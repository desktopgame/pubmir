# pubmir

秘密情報を含む **private リポジトリ** と、秘密情報を匿名化した **mirror リポジトリ** の間で、Git のコミット履歴を安全に搬送する CLI ツールです。

インフラ構成・設定・運用ドキュメントを、実際の IP アドレスやホスト名を Claude Code / Codex / GitHub に渡すことなく、AI と共同編集できるようにすることを目的としています。

## 何を解決するか

private リポジトリには実際の値が入っています。

```text
ssh actual-user@203.0.113.42
```

mirror リポジトリには、外部に出してよい表現だけが入ります。

```text
ssh <PUBMIR:VPS_USER>@<PUBMIR:VPS_PUBLIC_IP>
```

mirror は単なる一時ディレクトリではなく、**独立した Git リポジトリとして履歴を保持します**。そのため AI は `git log` / `git diff` / `git blame` を通じて、過去の設計変更や修正の経緯を通常どおり読むことができます。

匿名化はランダムな置換ではなく**安定した可逆トークン化**なので、同じ秘密値は時間が経っても常に同じトークンになります。AI から見ると `<PUBMIR:VPS_PUBLIC_IP>` は過去から現在まで一貫して同じ対象を指します。

```text
Private Git Repository
        │
        │ pubmir sync   （秘密値 → トークン）
        ▼
Sanitized Mirror Git Repository
        │
        │ Claude / Codex が編集・コミット
        │
        │ pubmir sync   （トークン → 秘密値）
        ▼
Private Git Repository
```

private と mirror は**完全に別の Git リポジトリ**です。`.git` を共有せず、mirror 側の Git オブジェクトデータベースには **sanitize 済みの commit / tree / blob しか書き込みません**（private の履歴をコピーしてから消す方式ではありません）。

## インストール

### go install

```bash
go install github.com/desktopgame/pubmir@latest
```

`$(go env GOPATH)/bin`（`GOBIN` を設定している場合はそちら）に `pubmir` がインストールされます。このディレクトリに PATH が通っていることを確認してください。

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

### ソースからビルド

```bash
git clone https://github.com/desktopgame/pubmir.git
cd pubmir
go build -o pubmir .
```

### 必要なもの

* Go 1.25 以降（ビルド時）
* `git` コマンド（実行時）— pubmir は Git を外部コマンドとして利用します

## クイックスタート

private と mirror を隣り合ったディレクトリに用意します。

```bash
# private 側
cd ~/work/VPS
git init -b main          # 既存リポジトリならそのままで可
pubmir init --role private --pair ../VPS-mirror

# mirror 側
cd ~/work/VPS-mirror
git init -b main
pubmir init --role mirror --pair ../VPS
```

private 側で秘密値を登録します。`.pubmir.env` は `pubmir init` が `.gitignore` に追加するので、Git には入りません。

```bash
cd ~/work/VPS
cat >> .pubmir.env <<'EOF'
VPS_PUBLIC_IP=203.0.113.42
VPS_USER=actual-user
EOF
```

private → mirror へ搬送します。

```bash
cd ~/work/VPS
pubmir sync
```

mirror 側で AI に作業してもらい、コミットまで済ませたら、private へ戻します。

```bash
cd ~/work/VPS-mirror
# Claude Code などで編集・コミット
pubmir sync          # 適用内容を表示して [y/N] 確認
```

mirror を GitHub 等へ push する前には `pubmir check` で漏洩がないか検査できます。

```bash
pubmir check
```

## コマンド

`pubmir sync` の意味は常に **「今いるリポジトリの未同期の変更を、対応するリポジトリへ安全に搬送する」** です。方向は `.pubmir.yml` の `role` から自動判定されるため、export / import や push / pull を覚え分ける必要はありません。

### `pubmir init --role <private|mirror> --pair <path>`

現在のリポジトリを pubmir 管理下に置きます。

* `.pubmir.yml`（role と除外設定、Git 管理対象）を作成
* `.pubmir/local.yml`（pair のパス、gitignore 対象）を作成
* `.gitignore` に pubmir 用のエントリを追記
* private の場合は `.pubmir.env` のテンプレートを作成
* mirror の場合は同梱の Claude Code skill を配置（後述）
* 上記のうち Git 管理対象のファイルを 1 つのコミットにまとめる

既に初期化済みの場合は、既存ファイルを上書きせずに停止します。

### `pubmir sync [--yes]`

未同期のコミットを、対応するリポジトリへ搬送します。

private → mirror では、秘密値をトークンへ変換し、除外対象を取り除き、コミットメッセージも同じマッピングで匿名化します。

mirror → private は private 側を書き換えるため、適用予定のコミット一覧を表示して確認を求めます。

```text
Current repository: mirror
Target repository:  private

3 commit(s) will be applied to PRIVATE:

  a1b2c3d  Clarify Nebula firewall documentation
  d4e5f6a  Update certificate rotation procedure
  012abcd  Fix Caddy configuration example

Proceed? [y/N]
```

`--yes` で確認を省略できます（非対話環境向け）。

### `pubmir status`

role・pair・両リポジトリの同期状態・コミット対応表を表示します。どちら側から実行しても構いません。

```text
Role: private
Pair: /home/user/work/VPS-mirror

Private:
  2 commit(s) not mirrored

Mirror:
  up to date

Stubbed files: 1

Commit mapping:
  private abc1234 <-> mirror def5678
```

`Stubbed files` は stub が設定されている場合のみ表示されます。

### `pubmir check`

mirror が外部へ出してよい状態かを検査します。どちら側から実行しても、検査対象は常に mirror です。

* private 側の秘密値（**過去にローテーションした値も含む**）が mirror の作業ツリーと**全履歴**に残っていないか
* ファイル名・ディレクトリ名に秘密値が含まれていないか
* private の除外設定に一致するファイルが混入していないか
* `.pubmir.env` / `.pubmir.secrets` が存在しないか（サブディレクトリも含む）
* mirror 自身のもの以外に `.git` という名前のパスがないか
* 未定義の `<PUBMIR:KEY>` トークンが残っていないか
* 一般的なシークレットパターン（PEM ヘッダ、AWS アクセスキー等）
* stub 対象ファイルが mirror に存在し、内容が placeholder のままか（欠落・改変を検出）
* stub 対象ファイルの private 側の実 blob が、mirror の object database に存在しないか

問題がなければ `OK: no leaks detected in mirror repository` を表示し、1 件でも見つかれば内容を列挙して終了コード 1 で終わります。

`blob <sha>:` が付いた指摘は**過去のコミットに埋まっている**ものです。作業ツリーを直しても消えないため、その場合は `pubmir rebuild` が必要になります（その旨も出力されます）。

### `pubmir rebuild [--yes]`

**private 側でのみ**実行できます。private の履歴を source of truth として、**現在のルールで mirror の履歴全体を最初から作り直します**。

`sync` は新しいコミットしか搬送しないため、匿名化のルール（`.pubmir.env` / `exclude` / `stub`）を後から追加しても、過去の mirror コミットに残った情報は消えません。それを消すためのコマンドです。

典型的な復旧手順:

```text
pubmir check            # 過去の履歴に漏洩を検出
    ↓
.pubmir.env / exclude / stub を更新
    ↓
pubmir rebuild          # 現在のルールで履歴を再生成
    ↓
pubmir check            # 解消を確認
    ↓
git push --force-with-lease   # 必要なら手動で
```

破壊的操作なので、既定で確認を求めます（`--yes` で省略可能ですが、安全性検査は省略されません）。

```text
WARNING: pubmir will rebuild the entire mirror history.

Private:
  /home/user/work/VPS

Mirror:
  /home/user/work/VPS-mirror

Branch:
  main

All mirror commit SHAs on this branch will change.

If this mirror has already been pushed to a remote repository,
a force push will be required afterward.

Proceed? [y/N]
```

次の場合は実行を拒否します。

* mirror 側に private へ未反映のコミットがある（それを捨ててしまうため）
* private / mirror のいずれかの作業ツリーが汚れている
* private の履歴にマージコミットがある
* mirror 側から実行された

pubmir が自動で push することはありません。remote が設定されている場合、完了後に `git push --force-with-lease` の案内を表示するだけです。

なお、オブジェクトの生成は決定論的なので、**ルールを変えずに rebuild しても結果は同じ**になります（何度実行しても安全です）。SHA が変わるのは、実際に内容が変わったコミット以降だけです。

## 設定ファイル

| ファイル | 場所 | Git 管理 | 内容 |
| --- | --- | --- | --- |
| `.pubmir.yml` | 両方 | **される** | `role`（private / mirror）と `exclude` / `stub` パターン。可搬な設定のみ |
| `.pubmir/local.yml` | 両方 | されない | `pair`（対になるリポジトリのパス）。クローン場所に依存するため分離 |
| `.pubmir.env` | private のみ | されない | `KEY=VALUE` 形式の秘密値。KEY が `<PUBMIR:KEY>` になる |
| `.pubmir/state.json` | 両方 | されない | commit mapping と、ブランチごとの最終同期 sha |
| `.pubmir/secrets-history.json` | private のみ | されない | pubmir が観測した秘密値の履歴（後述） |

`.pubmir.yml` の例:

```yaml
role: private
exclude:
  - "secrets/**"
  - "**/*.key"
  - "**/*.pem"
stub:
  - "config/production.yml"
```

`.git`、`.pubmir/`、`.pubmir.env`、`.pubmir.secrets` は、ユーザー設定に関わらず常に強制除外されます。

## stub ファイル

`exclude` はファイルの存在ごと mirror から消しますが、それでは AI が「そのファイルがある」ことすら分からなくなります。**stub** は、中身だけを隠してパスと存在は見せたい場合に使います。

```yaml
stub:
  - "config/production.yml"
  - "config/private/*.yml"
```

パターンの書式は `exclude` と同じです。mirror 側には同じパスでファイルが作られますが、中身は固定の placeholder に置き換わります。

```text
This file is intentionally stubbed by pubmir.
Its private contents are not available in the sanitized mirror.
```

placeholder には、元ファイルの内容・サイズ・ハッシュ・行数など、private 由来の情報を一切含みません。**private の blob は読み込まれもしないため、mirror の object database に（到達不能オブジェクトとしてすら）書き込まれません。**

パスが複数のルールに一致する場合の優先順位は次のとおりです。

```text
exclude > stub > tokenize
```

つまり `exclude` にも一致する場合はファイル自体が作られず、`stub` に一致する場合は placeholder に置き換わり、どちらでもなければ従来どおりトークン化されて搬送されます。

### stub は書き戻せません

stub は「匿名化された private ファイル」ではなく、**別個の安全な placeholder** です。そのため mirror → private の方向では、stub の内容が private へ書き戻されることは決してありません。

* mirror 側で stub が変更されていなければ、private の実ファイルはそのまま維持されます
* mirror 側で stub が**編集・削除・リネーム**された場合は、自動反映せず **sync を停止します**

```text
Error: stubbed file was modified in the mirror (commit 96bafcb...):

  config/production.yml

Stub files are not writable through pubmir. Refusing to apply this change to the private repository.
```

なお stub が隠すのは中身だけで、パスは mirror に公開されます。したがってパス文字列に秘密値が含まれる場合は、stub 対象であっても（従来どおり）sync を中断します。`config/203.0.113.42.yml` は「stub だから安全」とは扱いません。

## 秘密値のローテーションについて

`.pubmir.env` の値を変更すると、変更前の値を含む「まだ同期していない古いコミット」がトークン化されず漏洩する恐れがあります。これを防ぐため、pubmir は `.pubmir.env` を読み込むたびに現在値を `.pubmir/secrets-history.json` に追記し、**過去の値もすべて同じトークンへ変換します**。

ただしこれは pubmir が実際に観測した値のみのベストエフォートな記録です。pubmir を一度も実行せずに値を変更した場合や、履歴ファイルを失った場合は追跡できません。

### 秘密でなくなったキーを外すとき

`.pubmir.env` からキーを削除しただけでは足りません。履歴にその値が残っているため、pubmir はトークン化を続ける一方で `<PUBMIR:KEY>` を解決できなくなり、**sync / rebuild / check がすべて止まります**。

pubmir は「もう秘密でないので外した」のか「うっかり消した」のかを区別できず、間違えれば実値を公開してしまうため、勝手に判断せず次のように停止します。

```text
Error: secret key(s) DOMAIN were removed from .pubmir.env but their past values are
still recorded in .../.pubmir/secrets-history.json.

If they are genuinely no longer secret: delete those entries from secrets-history.json,
then run `pubmir rebuild` to regenerate the mirror without the tokens.
If they were removed by mistake: restore them to .pubmir.env.
```

本当に秘密でなくなった場合は、`.pubmir/secrets-history.json` から該当キーの項目を削除し（ローカル専用ファイルなので手で編集して構いません）、`pubmir rebuild` を実行してください。過去の履歴も含めてトークンが実値へ戻ります。

## Claude Code skill の同梱

`pubmir init --role mirror` を実行すると、mirror リポジトリに Claude Code 用の skill が配置されます。

```text
.claude/skills/pubmir-mirror/SKILL.md
```

このファイルはバイナリに埋め込まれており（`go:embed`）、初期化時のコミットに含まれるため、`git clone` した時点で共同作業者や AI エージェントに自動的に共有されます。既にファイルが存在する場合は上書きしません。

skill の内容は、AI に対して次のことを伝えます。

* private / mirror の境界とその意味
* `<PUBMIR:KEY>` トークンはそのまま保持し、実際の値を推測・検索・置換しないこと
* `pubmir status` と `pubmir check` は自由に実行してよいこと
* **`pubmir sync` は自分の判断で実行しないこと**（境界を越える判断は人間が行う）
* 対になる private リポジトリへアクセスしようとしないこと

## 安全機構

安全性は利便性より優先されます。次の場合、pubmir は自動処理を行わずに停止します。

* **divergence** — private と mirror の両方に未同期コミットがある（自動双方向マージは意図的に無効）
* **ブランチ名の不一致** — private と mirror で現在のブランチが異なる
* **履歴の書き換え** — 同期済みコミットが HEAD の祖先でなくなっている（rebase / amend / force-push）
* **履歴の消失** — 同期済みのブランチからコミットが失われている（削除 / reset）
* **マージコミット** — 未同期の範囲にマージコミットが含まれる（MVP では非対応）
* **作業ツリーが汚れている** — 適用先に未コミットの変更がある
* **パスに秘密値** — ファイル名やディレクトリ名に秘密値が含まれる（パスは匿名化せず、代わりに停止します）
* **未知のトークン** — mirror に、private 側で定義されていない `<PUBMIR:KEY>` がある
* **秘密値ファイルが Git 管理下** — `.pubmir.env` がコミットされている
* **stub の改変** — mirror 側で stub ファイルが編集・削除・リネームされている
* **stub 可能でないエントリ** — symlink や submodule が stub パターンに一致している
* **リポジトリ境界の異常** — private と mirror が同一ディレクトリ、入れ子、`.git` の共有、role の不一致、pair が相互を指していない

また private → mirror の同期では、生成したコミットを**どの ref からも参照されない loose object として作り切ってから**、全ファイルの内容とコミットメッセージを漏洩検査し、**問題がなければ初めて mirror の ref を進めます**。検査に失敗した場合、mirror の ref と作業ツリーは一切変更されません。

## 制限事項

MVP のため、以下は対応していません。

* **単一ブランチのみ** — private と mirror で現在チェックアウト中の同名ブランチのみを同期します
* **マージコミット非対応** — 未同期の範囲に含まれる場合は停止します
* **パス自体は匿名化しません** — ファイル名に秘密値が含まれる場合は、変換ではなく停止します
* **author / committer の名前とメールアドレスは変換されません** — 元のコミットの値がそのまま mirror に入ります
* **sync は pair 設定済みのローカルクローンから実行する必要があります** — GitHub から取得した別クローンでは、private が参照できないため sync できません（通常の `git pull` で手元の mirror に取り込んでから sync してください）
* **stub の内容はカスタマイズできません** — placeholder は固定文面で、拡張子に応じたコメント記法や独自テンプレートには対応していません
* **stub は書き込み不可です** — mirror 側の stub を通して private の設定を変更することはできません（stub を編集すると sync が停止します）
* **stub 可能なのは通常ファイルのみです** — symlink や submodule を stub 対象にするとエラーになります

* **`pubmir rebuild` は漏洩を取り消せません** — 現在の mirror 履歴からは消えますが、既に push / clone / fork / CI ログ等へ渡った複製までは消せません。認証情報が漏れた場合はローテーション・失効が本筋です

秘密鍵・アクセストークン・パスワードは、そもそも Git 管理しないことを前提としています。

## GitHub との関係

mirror を GitHub や GitLab に置くことを想定していますが、pubmir 自身はリモート操作や認証情報を管理しません。通常の `git push` / `git pull` を使ってください。

```text
private repo → pubmir sync → mirror repo → git push → GitHub (Private) → Claude / Codex
```

AI による変更は通常の `git pull` で手元の mirror に取り込んでから、`pubmir sync` で private へ戻します。

## 開発

```bash
go build ./...
go vet ./...
go test ./...
```

テストは実際に一時ディレクトリへ `git init` した本物のリポジトリを使い、private → mirror / mirror → private の往復、トークン化の安定性、除外、そして各種の安全機構（divergence、ブランチ不一致、履歴書き換え、パス経由の漏洩、未知トークンなど）を検証します。テストで使う値はすべてダミーです。
