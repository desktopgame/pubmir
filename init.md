# pubmir — Initial Implementation Instructions

> **重要: この `init.md` は初回の実装指示・初期方針を伝えるためだけのファイルである。**
>
> このファイルを継続的なメモ、作業ログ、進捗記録、TODO、設計変更履歴として使用してはいけない。
> 実装中または実装後に新しい情報を記録する必要がある場合は、用途に応じて `README.md`、`TODO.md`、`CHANGELOG.md`、`NOTES.md`、ADR 等の別ファイルを使用すること。
>
> **実装の進行に伴って、この `init.md` に内容を書き足したり、現在状態に合わせて更新したりしないこと。**
> このファイルは「最初に何を作るよう指示されたか」を残すための不変の初期指示として扱う。

---

# 1. プロジェクト概要

`pubmir` というCLIツールを実装する。

`pubmir` は、秘密情報を含む **private Git repository** と、秘密情報を匿名化した **mirror Git repository** の間で、Gitの変更履歴を安全に搬送するためのツールである。

主な用途は、private repository に含まれるインフラ構成・設定・運用ドキュメント等を、秘密情報をClaude Code / Codex / GitHub等に渡さずに共同編集できるようにすることである。

概念上は次の構造になる。

```text
Private Git Repository
        │
        │ pubmir sync
        │ sanitize / tokenize
        ▼
Sanitized Mirror Git Repository
        │
        │ Claude / Codex edits
        │ git commits
        │
        │ pubmir sync
        │ restore tokens
        ▼
Private Git Repository
```

private と mirror は**完全に別のGit repository**とする。

`.git` ディレクトリやGit object databaseを共有してはいけない。

mirror側のGit履歴から、private側の過去のblobや秘密情報へアクセスできないことが重要である。

---

# 2. 基本思想

## 2.1 private repository

private repository が実値を持つ。

例:

```text
ssh actual-user@203.0.113.42
```

private repositoryには、必要に応じて以下のような情報が存在しうる。

* 実際のIPアドレス
* ホスト名
* ユーザー名
* 内部ネットワークアドレス
* ドメイン
* インフラ構成情報
* その他、AIや外部サービスへ渡したくない識別情報

秘密鍵、アクセストークン、パスワード等については、そもそもGit管理しないことを基本とする。

---

## 2.2 mirror repository

mirror repository はAIや外部サービスへ渡してよい表現だけを持つ。

例:

```text
ssh <PUBMIR:VPS_USER>@<PUBMIR:VPS_PUBLIC_IP>
```

mirrorは単なる一時ディレクトリではなく、**独立したGit repositoryとして履歴を保持する**。

Claude CodeやCodexはmirror repository上で作業できる。

そのため、以下の情報がAIにとって利用可能になる。

* `git log`
* `git diff`
* `git blame`
* 過去の設計変更
* 過去のAIによる修正
* コミットメッセージ
* 文書や設定の変遷

mirror repositoryはGitHub等のremote repositoryへpushできることを想定する。

ただし、`pubmir` のMVP自体がGitHub APIやremote操作を管理する必要はない。

通常の `git push` / `git pull` を利用すればよい。

---

# 3. 匿名化方式

匿名化はランダムな置換ではなく、**安定した可逆トークン化**を使用する。

例:

```text
203.0.113.42
↓
<PUBMIR:VPS_PUBLIC_IP>
```

```text
actual-user
↓
<PUBMIR:VPS_USER>
```

同じ秘密値は、時間が経過しても同じトークンへ変換されること。

これによりmirror側の履歴上でも、AIは

```text
<PUBMIR:VPS_PUBLIC_IP>
```

が過去から現在まで同じ対象を表していると理解できる。

mirror → private方向では、このトークンを元の実値へ戻す。

---

# 4. 設定

各repositoryには `pubmir` 用設定を置き、そのrepositoryがどちら側なのかを明示する。

設定ファイル名は原則として:

```text
.pubmir.yml
```

とする。

例:

```yaml
role: private
pair: ../VPS-mirror
```

mirror側:

```yaml
role: mirror
pair: ../VPS
```

実際の秘密値そのものは `.pubmir.yml` に書かない。

秘密値はGit管理対象外の別ファイルで管理する。

例:

```text
.pubmir.secrets
```

または

```text
.pubmir.env
```

形式は実装上扱いやすいものを選んでよい。

例:

```env
VPS_PUBLIC_IP=203.0.113.42
VPS_USER=actual-user
NEBULA_LIGHTHOUSE_IP=192.0.2.10
PRIVATE_DOMAIN=example.internal
```

private側の `.gitignore` には必ず秘密値ファイルを追加する。

---

# 5. コマンド

MVPでは以下のコマンドを実装する。

```text
pubmir init
pubmir sync
pubmir status
pubmir check
```

不要にサブコマンドを増やさないこと。

特に `export` / `import` のような、方向を誤認しやすい名称は使用しない。

---

# 6. `pubmir init`

現在のrepositoryをpubmir管理下に置くための初期設定を行う。

必要に応じて以下を行う。

* `.pubmir.yml` の作成
* private / mirror roleの設定
* paired repositoryの設定
* private側で秘密値ファイルのひな形を作成
* `.gitignore` の設定確認
* 必要なpubmir内部管理領域の作成

既存ファイルを不用意に上書きしてはいけない。

すでに設定が存在する場合は安全に停止するか、明示的に確認すること。

---

# 7. `pubmir sync`

`sync` は**現在いるrepositoryをsourceとして、paired repositoryへ変更を搬送する**。

方向は `.pubmir.yml` の `role` から自動判定する。

ユーザーが方向をコマンド引数で指定する必要はない。

---

## 7.1 private repositoryから実行

```text
private
$ pubmir sync
```

意味:

```text
private → mirror
```

行う処理:

1. private側の未同期変更・コミットを特定する
2. Git管理対象となる内容を取得する
3. 秘密値を安定した `<PUBMIR:...>` トークンへ変換する
4. 除外対象ファイルを除く
5. コミットメッセージ等、同期するGitメタデータについても必要なら匿名化する
6. mirror repositoryへ対応する変更を適用する
7. mirror側に新しいコミットを作成する
8. private commit と mirror commit の対応関係を記録する
9. 漏洩チェックを行う

private側のGit objectをmirror側へコピーしてはいけない。

---

## 7.2 mirror repositoryから実行

```text
mirror
$ pubmir sync
```

意味:

```text
mirror → private
```

主にClaude Code / Codex等がmirror側で作成したコミットをprivate側へ反映する。

行う処理:

1. mirror側の未同期コミットを特定する
2. `<PUBMIR:...>` トークンをprivate値へ戻す
3. private repositoryへ変更を適用する
4. private側に対応するコミットを作成する
5. commit mappingを記録する

この方向はprivate repositoryを書き換えるため、private → mirrorより慎重に扱う。

**デフォルトで適用内容をユーザーへ明示し、確認なしでprivate repositoryを破壊的に変更しないこと。**

可能なら以下のような表示を行う。

```text
Current repository: mirror
Target repository:  private

3 commits will be applied to PRIVATE:

  a1b2c3d  Clarify Nebula firewall documentation
  d4e5f6a  Update certificate rotation procedure
  012abcd  Fix Caddy configuration example

Proceed? [y/N]
```

非対話環境向けの明示的なオプションは将来的に追加してよいが、安全側をデフォルトとする。

---

# 8. `pubmir status`

両repositoryの同期状態を表示する。

例:

```text
Role: private
Pair: D:\AI-Work\VPS

Private:
  2 commits not mirrored

Mirror:
  up to date
```

または:

```text
Private:
  up to date

Mirror:
  3 commits not applied to private
```

commit mappingも確認可能にする。

例:

```text
private abc1234 <-> mirror def5678
private 111aaaa <-> mirror 222bbbb
```

表示形式は実装上改善してよい。

---

# 9. `pubmir check`

mirror repositoryが外部へ出してよい状態か検査する。

最低限、以下を確認する。

* private側で設定された秘密値がmirror側に残っていない
* private keyらしきファイルが存在しない
* 除外対象ファイルがmirror側へ混入していない
* `.pubmir.secrets` / `.pubmir.env` 等がmirrorへコピーされていない
* private repositoryの `.git` がコピーされていない
* 変換不能または不正なPUBMIRトークンがない

可能なら、一般的なsecret patternについても検査する。

ただし、一般的なsecret scannerだけに依存してはいけない。

本ツールの重要な目的の一つは、

```text
IP address
hostname
username
internal domain
```

のような、通常のsecret scannerでは秘密扱いされない情報もユーザー指定により匿名化することである。

---

# 10. 除外ルール

設定からファイル・ディレクトリをmirror対象外にできるようにする。

例:

```yaml
exclude:
  - ".git/**"
  - "secrets/**"
  - "**/*.key"
  - "**/*.pem"
  - ".pubmir.env"
  - ".pubmir.secrets"
```

`.git` とpubmirの秘密値ファイルについては、ユーザー設定に関係なく安全上強制除外してもよい。

---

# 11. divergence

両repositoryに未同期コミットが存在する場合、pubmirが勝手に双方向マージしてはいけない。

例:

```text
private: +2 commits
mirror:  +3 commits
```

この状態では原則停止する。

例:

```text
Both repositories contain unsynchronized changes.

Automatic bidirectional merge is intentionally disabled.

Run `pubmir status` and resolve the divergence before syncing.
```

Gitのmerge/rebase機能をpubmir独自に再実装しないこと。

pubmirの責務は、

> 秘密境界を越えてコミットを安全に変換・搬送すること

であり、一般的なGit DVCSそのものを置き換えることではない。

---

# 12. commit mapping

privateとmirrorでは内容が変換されるため、commit SHAは一致しない。

そのため対応関係をpubmir内部で保持する。

例:

```text
private abc123
mirror  7def89
```

この対応情報は、

* どこまで同期済みか
* 次に搬送すべきコミットは何か
* 二重適用を防ぐ
* status表示

に利用する。

mapping自体に秘密情報を含めないこと。

mirror側へ置かれるmetadataは、外部へ公開されても秘密情報が漏れない形式にする。

---

# 13. 安全性

安全性は機能追加より優先する。

最低限以下を守る。

## 13.1 repository境界

以下の場合は処理を拒否する。

* privateとmirrorが同一directory
* 一方がもう一方の `.git` を共有している
* 明らかに不正なpair設定
* repository roleが判定できない

---

## 13.2 秘密値

秘密値ファイルはGit管理しない。

`pubmir` は秘密値ファイルがGit indexに入っていることを検出した場合、警告または処理停止すること。

---

## 13.3 mirrorからprivateへの適用

mirror → private はprivate側を書き換えるため、適用対象を明示する。

曖昧な状態では止まる。

「おそらくこうだろう」と推測してprivateを書き換えない。

---

## 13.4 不明なトークン

mirror側に、

```text
<PUBMIR:SOMETHING>
```

が存在するが対応するprivate値が定義されていない場合、勝手に文字列を残したままprivateへ適用しない。

エラーとして停止する。

---

# 14. GitHubとの関係

mirror repositoryはGitHub、GitLab等へ置けることを想定する。

ただしMVPではpubmir自身がremote repositoryを作成したり、認証情報を管理したりする必要はない。

通常のGit操作を利用する。

例:

```text
private repo
    ↓
pubmir sync
    ↓
mirror repo
    ↓
git push
    ↓
GitHub Private
    ↓
Claude / Codex
```

GitHubから取得したAIの変更も通常のGitでmirrorへpullした後、

```text
mirror
$ pubmir sync
```

でprivateへ適用できればよい。

---

# 15. Git履歴について

mirror側では、可能な限りprivate側の変更単位を維持する。

例えばprivate側の:

```text
A -- B -- C
```

はmirror側でも:

```text
A' -- B' -- C'
```

となることが望ましい。

ただしSHAは異なる。

重要なのは、AIが履歴を理解できることである。

private repositoryの履歴を一度コピーしてから秘密情報を削除する方式は避ける。

**最初からsanitizedされたcommit / tree / blobのみをmirror側へ生成すること。**

mirror側のGit object databaseにprivate値を一度でも書き込まない設計を優先する。

---

# 16. コミットメッセージ

コミットメッセージにも秘密情報が含まれる可能性がある。

private → mirror時には、ファイル本文と同じ秘密値マッピングをコミットメッセージにも適用する。

例:

```text
Fix SSH connection to 203.0.113.42
```

↓

```text
Fix SSH connection to <PUBMIR:VPS_PUBLIC_IP>
```

mirror → private時にコミットメッセージまで逆変換するかどうかは、実装時に安全で自然な方法を選ぶ。

少なくともmirror上にprivate値が漏れないことを優先する。

---

# 17. MVPでやらないこと

初期実装を過剰に複雑化しない。

MVPでは以下を必須としない。

* GUI
* Web UI
* GitHub API integration
* GitLab API integration
* daemon / watch mode
* 自動 `git push`
* 自動 `git pull`
* 独自merge algorithm
* 自動conflict resolution
* repository全体の暗号化
* secrets manager
* cloud storage
* 複数mirrorへの同時配布

必要になれば後で追加する。

---

# 18. 実装方針

CLIとして扱いやすく、Windows / Linuxの双方で動かせる構成を優先する。

新規実装で特に既存の言語指定がない場合、単一バイナリ化しやすい **Go** を第一候補としてよい。

ただし、このrepositoryにすでに明確な実装言語・toolchain・project scaffoldが存在する場合は、それを尊重する。

外部コマンドとしてGitを利用する実装でもよい。

独自Git実装を作る必要はない。

---

# 19. テスト

安全性に関係する処理は自動テストを用意する。

最低限、以下をテストする。

### private → mirror

* 秘密値がトークンになる
* 同じ値は常に同じトークンになる
* 除外ファイルがコピーされない
* private `.git` のblobがmirrorへ入らない
* commit mappingが生成される
* コミットメッセージが匿名化される

### mirror → private

* トークンが正しい実値へ戻る
* AIが編集した通常テキストが維持される
* 未知のトークンがあれば停止する
* private側へ意図しない削除を行わない
* 二重import相当の処理を防止する

### safety

* source == targetを拒否
* repository内包関係等の危険な設定を拒否
* secret fileがGit管理されていたら検出
* divergence時に自動同期しない

テストでは実際の秘密情報を使用せず、明らかなダミー値を使用する。

---

# 20. 初期UXの目標

ユーザーが日常的に覚える必要がある操作を極力少なくする。

private側:

```bash
pubmir status
pubmir sync
```

mirror側:

```bash
pubmir status
pubmir sync
```

同じコマンドをどちらで実行しても、`.pubmir.yml` のroleから意味が決まる。

ユーザーが

```text
exportなのかimportなのか
pushなのかpullなのか
```

を覚える必要がないことが重要である。

`pubmir sync` の意味は常に:

> **現在いるrepositoryの未同期変更を、対応するrepositoryへ安全に搬送する**

で統一する。

---

# 21. 最重要要件

このプロジェクトで最も重要なのは、便利さよりも以下である。

1. privateの秘密情報がmirrorへ漏れない
2. private Git履歴そのものがmirrorから参照できない
3. mirror側では有用なGit履歴が維持される
4. Claude / Codexがmirrorを通常のGit repositoryとして扱える
5. AI側の変更を手作業でコピーせずprivateへ戻せる
6. 方向を間違えにくいCLIである
7. 曖昧・危険な状況では自動処理せず停止する

設計判断に迷った場合は、この順序を優先する。

---

# 22. 実装開始時

まず既存repositoryの内容を確認し、既存コードがあればその構造を把握する。

その後、

1. データモデル
2. `.pubmir.yml`
3. secret mapping
4. repository role/pair detection
5. commit mapping
6. `status`
7. private → mirror `sync`
8. `check`
9. mirror → private `sync`
10. safety tests

程度の順序で実装を進める。

実装過程の進捗や追加判断を**この `init.md` に追記しないこと**。

必要なら別の適切なファイルを作成すること。
