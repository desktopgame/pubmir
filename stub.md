# pubmir — Stub File Support Implementation Instructions

## Purpose

`pubmir` に **stub file** 機能を追加する。

stubは、private repository側では実ファイルの内容を一切mirrorへ出したくないが、

* そのファイルが存在すること
* そのパス
* その役割
* 必要に応じて最低限の説明

はAI側に見せたい場合に使用する。

これは通常のtokenizationとは異なり、**private fileの内容を読んで匿名化するのではなく、mirror側では完全に別の安全な内容へ置き換える**機能である。

---

## Basic behavior

private repository:

```text
config/
  production.yml
```

`production.yml` がstub対象に指定されている場合、private側の実内容はmirrorへ搬送しない。

mirror repositoryには同じパスで、

```text
config/
  production.yml
```

を作成するが、中身はpubmirが生成したstub内容とする。

例:

```text
# This file is intentionally stubbed by pubmir.
# The private repository contains the real configuration.
```

private側の実blobをmirror側のGit object databaseへ書き込んではならない。

---

## Configuration

既存のgit管理対象 `.pubmir.yml` に `stub` セクションを追加する。

例:

```yaml
role: private

exclude:
  - "secrets/**"
  - "**/*.key"

stub:
  - "config/production.yml"
  - "config/private/*.yml"
```

glob matchingは既存の`exclude`と同じルールを利用する。

`**` を含むパターンにも対応する。

---

## Default stub content

stub対象ファイルのmirror側内容は、MVPではpubmirが固定文面を生成する。

例:

```text
This file is intentionally stubbed by pubmir.
Its private contents are not available in the sanitized mirror.
```

必要ならコメント記法をファイル種別に合わせるような高度な処理は将来対応とし、MVPでは必須としない。

固定内容は秘密情報を一切含まないこと。

private fileの:

* 内容
* サイズ
* hash
* MIME推定結果
* 行数
* private値

などをstub本文へ埋め込まないこと。

---

## Precedence

パスが複数ルールに一致する場合、優先順位を明確にする。

以下とする:

```text
exclude > stub > tokenize
```

つまり:

### excludeに一致

mirrorにはファイル自体を作らない。

### stubに一致

mirrorには同じパスでstub fileを作る。

private内容にtokenizationは行わない。

### どちらにも一致しない

従来通りprivate fileを読み、tokenizationしてmirrorへ搬送する。

---

## Security requirements

stub対象ファイルについては、private fileのblobをmirror repositoryへ一度も書き込まない。

処理順として、

```text
path classification
    ↓
exclude?
    → skip

stub?
    → generate safe stub blob

otherwise
    → read private blob
    → tokenize
```

とする。

**stub判定前にprivate blobをmirror側へコピーしてから上書きする実装は禁止する。**

mirror Git object databaseにprivate内容を含むunreachable blobが残ることも許容しない。

---

## Path handling

stubはファイル内容のみを隠す。

ファイルパス自体はmirrorへ公開される。

そのため、既存仕様どおり、パス文字列に登録済みsecret valueが含まれている場合はstub対象であってもsyncを中断する。

例:

```text
config/203.0.113.42.yml
```

が秘密値を含む場合、

```text
stubだから安全
```

とは判断しない。

パス漏洩チェックを先に行うこと。

---

## private → mirror sync

private → mirror時、各tree entryについて:

1. pathが強制除外または`exclude`に一致するか確認
2. pathにsecret valueが含まれていないか確認
3. `stub`に一致するか確認
4. stubの場合:

   * private blob内容をmirrorへ搬送しない
   * fixed stub contentから新しいblobを生成する
   * 元ファイルと同じpathでmirror treeへ配置する
5. stubでなければ従来のtokenization処理を行う

Git file modeは可能な限り元ファイルのmodeを維持する。

ただしsymlink、submodule等の特殊entryがstubに一致する場合の扱いは安全側に倒すこと。

MVPでは通常ファイルのみstub可能とし、特殊entryは明示的エラーでもよい。

---

## mirror → private sync

ここは重要。

mirror側のstub fileは**private側の実ファイルを表す編集対象ではない**。

したがってmirror → private sync時に、stub対象パスのmirror内容をprivate fileへ書き戻してはいけない。

基本ルール:

```text
stub path is one-way protected content
```

mirror → private時:

* stub対象パスがmirror側で変更されていない場合:

  * private側の実ファイルをそのまま維持する
  * mirrorのstub内容をprivateへ適用しない

* stub対象パスがmirror側で変更された場合:

  * 自動反映しない
  * syncを停止して人間へ報告する

例:

```text
Stubbed file was modified in the mirror:

  config/production.yml

Stub files are not writable through pubmir.
Refusing to apply this change to the private repository.
```

stub fileの削除・renameについても同様に、MVPでは自動反映しない。

安全側に停止する。

---

## Detecting mirror-side stub modifications

pubmirは、mirror側に生成した期待されるstub内容と現在の内容を比較できるようにする。

mirror → private sync前に、stub対象pathについて:

* expected stub file exists
* content equals generated stub
* path remains unchanged

を確認する。

一致しない場合は「AIまたは人間がstubを編集した」とみなし、syncを拒否する。

---

## check command

`pubmir check` にstub検査を追加する。

最低限以下を確認する:

1. stub対象pathがmirrorに存在すること
2. 内容が期待されるstub contentと一致すること
3. private実内容がmirror blob/historyへ混入していないこと
4. stub pathにsecret valueが含まれていないこと
5. stub対象fileがmirror側で通常内容へ置き換えられていないこと

問題がある場合はfailure扱いにする。

---

## status

`pubmir status` にstub数を表示してもよい。

例:

```text
Stubbed files: 3
```

ただしMVPでは必須ではない。

---

## init behavior

`pubmir init` のデフォルト `.pubmir.yml` にstubの例をコメント付きで追加してもよい。

例:

```yaml
stub: []
```

または:

```yaml
# stub:
#   - "config/production.yml"
```

既存設定との後方互換性を維持し、`stub` が存在しない場合は空リストとして扱う。

---

## Tests

最低限以下を追加する。

### private → mirror

* stub対象fileのprivate内容がmirrorに出現しない
* mirrorには同じpathでstub contentが存在する
* private blobがmirror object databaseへ書き込まれない
* `exclude`と`stub`両方に一致した場合はexcludeが優先される
* stub pathにsecret valueが含まれる場合はsync失敗

### mirror → private

* untouched stub fileはprivate実fileを上書きしない
* mirror側でstub内容を変更するとsync失敗
* stub fileを削除するとsync失敗
* stub fileをrenameするとsync失敗または安全側に検出・拒否する
* private fileの実内容が維持される

### check

* 正常stubはpass
* 内容変更済みstubはfail
* 欠落stubはfail
* private内容がmirror履歴へ混入していたらfail

---

## Non-goals for MVP

以下は今回実装しない。

* user-defined stub content
* file extensionごとのコメント記法
* automatic sanitized templates
* stub contentからprivate内容へのreverse generation
* writable stub
* stub fileを通したAIからprivateへの設定変更
* directoryそのものを特殊なplaceholderへ変換する機能

必要になれば将来追加する。

---

## Design principle

stubの意味は:

> **The file exists, but its private contents do not cross the pubmir boundary.**

stubは「匿名化されたprivate file」ではない。

**private fileとは別の、安全なplaceholder artifactである。**

そのため、stub内容をprivateへreverse syncしてはいけない。

安全性に迷った場合は、private fileを変更せずsyncを停止すること。
