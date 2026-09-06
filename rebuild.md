# pubmir — Mirror History Rebuild Implementation Instructions

## Purpose

`pubmir` に **mirror history rebuild** 機能を追加する。

この機能は、mirror repository 作成後に新しいsecretや匿名化対象の漏れに気づき、`.pubmir.env` / secret history / stub / exclude 等のルールを更新した場合に使用する。

通常の `pubmir sync` は新規コミットのみを搬送するため、過去のmirror commit/blobに存在する漏洩情報は消えない。

`rebuild` は、現在のprivate repositoryの履歴をsource of truthとして、**現在のpubmirルールを使ってmirrorの履歴全体を最初から再生成する**。

---

## Command

追加するコマンド:

```bash
pubmir rebuild
```

`rebuild` は通常syncとは異なる破壊的操作である。

意味:

```text
private history
A -- B -- C -- D
        ↓
current pubmir rules
        ↓
new mirror history
A'-- B'-- C'-- D'
```

既存のmirror commit SHAは原則すべて変わる。

---

## Direction

`rebuild` は **private → mirror 専用** とする。

mirror側から実行された場合は停止し、private repository側で実行するよう案内する。

例:

```text
pubmir rebuild must be run from the private repository.
```

mirror → private のrebuildは実装しない。

---

## Preconditions

`rebuild` 実行前に以下をすべて確認する。

### Repository roles

* current repository が `role: private`
* paired repository が `role: mirror`
* pair設定が相互に一致
* repository boundary safety checks が通る

### Branch

MVPでは通常syncと同様、現在branchのみ対応する。

private / mirrorの現在branch名が一致していること。

unborn mirror branchについてはbootstrap rebuildとして許可してよい。

### Working trees

private / mirrorの両方がcleanであること。

未コミット変更が存在する場合は停止する。

### Unsynced mirror changes

mirror側に、privateへまだ反映されていないcommitが存在する場合は **rebuildを拒否する**。

例:

```text
Mirror contains 2 commits that have not been synchronized back to private.

Rebuild would discard these commits.

Synchronize or otherwise resolve them before rebuilding.
```

自動でrebase/cherry-pick/importしてはいけない。

### History rewrite state

既存stateが壊れている場合でも、rebuild自体は復旧手段になり得る。

そのため、通常syncの`last_synced_sha ancestor`条件はrebuildでは必須にしなくてよい。

ただし、private repositoryの現在HEADおよび履歴が正常に読めることは必要。

---

## User confirmation

`rebuild` はmirror履歴を置き換えるため、デフォルトで明示確認を要求する。

例:

```text
WARNING: pubmir will rebuild the entire mirror history.

Private:
  D:\VPS

Mirror:
  D:\VPS-mirror

Branch:
  main

All mirror commit SHAs on this branch will change.

If this mirror has already been pushed to a remote repository,
a force push will be required afterward.

Proceed? [y/N]
```

デフォルトは `N`。

非対話用途として:

```bash
pubmir rebuild --yes
```

を許可してよい。

ただし `--yes` でも安全性検査を省略してはいけない。

---

## Rebuild algorithm

rebuildはprivate側の現在branchの到達可能な履歴をroot commitから順に再構築する。

### 1. Enumerate private history

private branchについてrootからHEADまでのcommitを取得する。

可能であれば既存syncengineのprivate → mirror変換ロジックを再利用する。

### 2. Apply current pubmir policy

各commitについて現在の:

* `.pubmir.env`
* `.pubmir/secrets-history.json`
* `exclude`
* `stub`
* path leak rules
* tokenization rules
* commit message tokenization
* other current sanitization policy

を適用する。

重要:

> 過去commit作成当時のpubmir設定ではなく、**rebuild実行時点の現在ルール**を履歴全体へ適用する。

これがrebuildの主要目的である。

### 3. Build new objects without updating refs

通常syncと同様、最初はmirror Git object databaseへ新しいsanitized blob/tree/commitを生成するだけにし、branch refは更新しない。

全履歴の再生成が完了するまで既存mirror branchを変更しない。

### 4. Full leak check

生成された新しいmirror history全体に対し、現在の `pubmir check` 相当の厳格な検査を行う。

最低限:

* current secret valuesが存在しない
* historical secret valuesが存在しない
* secret valuesがpathに存在しない
* excluded filesが存在しない
* stub filesが期待された安全内容になっている
* unknown `<PUBMIR:...>` tokensが存在しない
* private blobがmirror historyへ混入していない

1件でもfailureがあれば、既存mirror refを変更せず中断する。

### 5. Atomically replace mirror branch ref

全検査成功後のみ、mirror branch refを新しいtipへ更新する。

可能な限り:

```bash
git update-ref refs/heads/<branch> <new-tip> <expected-old-tip>
```

を使い、rebuild中にmirror refが他プロセスから変更されていた場合は失敗させる。

その後working treeを新tipへ合わせる。

### 6. Replace mapping state

既存のprivate↔mirror commit mappingは無効になる。

現在branchについてmappingを新しい履歴に完全置換する。

例:

```text
old:
private A <-> mirror X
private B <-> mirror Y

new:
private A <-> mirror P
private B <-> mirror Q
```

`last_private_sha` / `last_mirror_sha` も新tipに更新する。

両repositoryのlocal stateを同一内容へ更新する。

---

## Merge commits

rebuildのmerge commit対応方針は通常syncと一致させる。

MVPがmerge commit非対応なら、private履歴内にmerge commitを検出した時点でrebuildを拒否する。

例:

```text
Cannot rebuild: merge commit detected in private history.

Merge commits are not supported by the current pubmir version.
```

履歴を暗黙に線形化してはいけない。

---

## Remote repositories

`pubmir rebuild` 自体はremoteへpushしない。

rebuild後にremote mirrorが存在する場合、ユーザーが通常のGitで履歴を置き換える。

例:

```bash
git push --force-with-lease
```

`pubmir` が自動でforce pushしてはいけない。

rebuild完了時には、remoteが設定されている場合に案内を表示してよい。

例:

```text
Mirror history rebuilt successfully.

Existing remote history may still contain the old sanitized history.

If appropriate, update the remote manually with:

  git push --force-with-lease
```

remote URLや認証情報を不要にログへ表示しないこと。

---

## Security warning

rebuildは**漏洩を未発生に戻す機能ではない**。

mirrorがすでに外部へpushされていた場合、古い情報が:

* clone
* fork
* cache
* CI log
* archive
* other external storage

等に残っている可能性がある。

そのため、rebuild完了時に以下の趣旨を表示する。

```text
Important:
Rebuilding removes the value from the current mirror history,
but cannot guarantee removal from external copies.

If the leaked value was a credential, key, token, password,
or other revocable secret, rotate or revoke it.
```

特にcredential系については、rebuildをsecret rotationの代替として扱わない。

---

## Interaction with `pubmir check`

`pubmir check` は引き続きmirrorの全到達可能履歴を検査する。

典型的な復旧フロー:

```text
pubmir check
    ↓
historical leak detected
    ↓
update .pubmir.env / stub / exclude
    ↓
pubmir rebuild
    ↓
pubmir check
    ↓
manual force push if needed
```

必要なら `check` のerror messageに:

```text
Historical leak detected.
After updating pubmir rules, `pubmir rebuild` may be required.
```

という案内を追加する。

---

## Interaction with secret history

rebuildでは `.pubmir/secrets-history.json` の全値を使用する。

たとえば:

```json
{
  "VPS_PUBLIC_IP": [
    "1.2.3.4",
    "5.6.7.8"
  ]
}
```

なら、privateのどの過去commitに現れても両方を:

```text
<PUBMIR:VPS_PUBLIC_IP>
```

へ変換する。

ただし、pubmirが一度も観測しておらずhistoryにも登録されていない値を自動的に知ることはできない。

これは既存のsecret-history仕様と同じ制限である。

---

## Failure behavior

rebuild途中で以下が発生した場合:

* tokenization error
* path leak
* unknown token
* invalid stub
* unsupported Git object
* merge commit
* leakcheck failure
* ref race
* user cancellation

既存mirror branch refおよびworking treeは変更しない。

新しく作成されたunreferenced loose objectがmirror ODBに残ることは許容する。

Git GCにより後で回収される。

state fileも成功するまで更新しない。

---

## Tests

最低限以下を追加する。

### Successful rebuild

private:

```text
A -- B -- C
```

mirror:

```text
A'-- B'-- C'
```

匿名化ルール変更後にrebuildすると:

```text
A''-- B''-- C''
```

へ全履歴が置き換わること。

### Historical secret cleanup

1. secret valueを登録せずprivate commit作成
2. mirrorへsyncして漏洩させる
3. valueをpubmir mapping/historyへ追加
4. `pubmir rebuild`
5. mirror全履歴からその値が消える
6. `<PUBMIR:KEY>`へ置換されている

### Existing mirror AI commits

mirrorに未同期commitがある場合はrebuildを拒否し、そのcommitが失われないこと。

### Failure atomicity

rebuild途中のleakcheck failure等で:

* mirror branch SHAが変化しない
* mirror working treeが変化しない
* state mappingが変化しない

こと。

### Mapping replacement

rebuild後、すべてのprivate commitに新しいmirror SHA mappingが存在すること。

旧mirror SHA mappingが現在branch stateから消えること。

### Stub rules

stub追加後にrebuildすると、過去の全commitでも対象file内容がstubへ置換され、過去のprivate blobが新しい到達可能mirror履歴へ含まれないこと。

### Exclude rules

exclude追加後にrebuildすると、過去の全commitから対象pathが消えること。

### Remote safety

rebuildが自動push / force pushを一切行わないこと。

---

## Non-goals

今回の実装では以下を行わない。

* automatic force push
* remote history deletion
* GitHub/GitLab APIによるcache purge
* credential rotation
* backup deletion
* fork deletion
* external clone tracking
* mirror-only commitの自動保全
* automatic merge/rebase of unsynchronized mirror work

---

## Design principle

`sync` と `rebuild` の責務を混同しない。

```text
pubmir sync
    = 新しい変更を境界越しに搬送する

pubmir rebuild
    = 現在の安全ルールを使ってmirror履歴全体を再生成する
```

`rebuild` のsource of truthは常にprivate repositoryである。

mirrorに未同期の独自変更がある場合は、失わせずに停止する。

安全性に迷った場合は既存mirror履歴を保持したまま処理を中断すること。
