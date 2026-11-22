# Flick

[English](README.md) | [한국어](README.ko.md) | **日本語**

Flickは、機密情報やファイルを1回限りのリンクで受け渡すためのセルフホスト型オープンソースサービスです。内容はアップロード前にブラウザやCLIで暗号化され、一度開かれるか有効時間が過ぎるとサーバーから削除されます。

**ライブデモ：** https://flick.makana.cc/ · **使い方・開発者ガイド：** https://flick.makana.cc/guides/

## 主な機能

- **1回限りの暗号化リンク：** メモ、ファイル（複数ファイルやフォルダーは1つに圧縮）、構造化テンプレート（ログイン、カード、個人情報、カスタム）を、5分から7日の有効時間で送れます。
- **任意のパスワード保護：** パスワードなしのリンクは鍵をURLフラグメント（`#key=...`）に含めるため、サーバーへ送信されません。パスワードありの場合はリンクとパスワードの両方が必要になり、5回間違えるとサーバーから削除されます。
- **リアルタイム転送：** 6桁のコードと3桁の確認番号を使って、開いている2つの端末間でテキストやファイルを直接やり取りできます。暗号文はAPIのメモリ上を通過するだけで、データベースやストレージには書き込まれません。
- **リンクでリクエスト：** 相手に渡すリンクと自分用の受け取りリンクを同時に発行し、相手から最初に送られた内容だけを1回開けます。詳しい仕様は[リクエストリンク仕様](docs/architecture/request-links.md)を参照してください。
- **アカウント不要の管理リンク：** ログインなしで未開封の確認や取り消しができます。管理リンクは配信の期限とともに失効し、内容を開いたり、なくした受け取り人用リンクを復元したりはできません。Flickは送信履歴を残さないため、必要なリンクは各自で保存してください。
- **柔軟なストレージと非同期削除：** 小さな暗号化データはPostgreSQL内に直接保存し、大きなファイルはS3互換ストレージに置けます（OCIとCloudflare R2のように複数バケットを上限付きで併用可能）。開封済み・期限切れのデータは非同期ワーカーが削除します。
- **多言語Web UI：** 英語・韓国語・日本語に対応しています。

## セキュリティモデル

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

- **端末内での暗号化：** ブラウザとCLIは[age](https://age-encryption.org/)形式を使い、送信ごとに新しい耐量子ハイブリッド鍵（`ML-KEM-768` + `X25519`）で暗号化します。大きなファイルは64 KiBのチャンクに分割して暗号化し、最後のチャンクの検証が終わるまで表示しません。
- **鍵の扱い：** パスワードなしの場合、鍵はURLフラグメント（`#key=AGE-SECRET-KEY-PQ-1…`）のみを通るため、HTTPリクエストやアクセスログに残りません。パスワードありの場合、鍵をageの`scrypt`モード（work factor 18）でラップし、別のアクセス証明（`PBKDF2-SHA-256`、600,000回反復）を導出して、サーバーにはそのハッシュだけを保存します。内蔵の生成機能は80ビットのパスワードを作成します。
- **リアルタイム転送：** 受け取る側のブラウザが1回限りの鍵ペアを生成して公開鍵だけを送り、送る側がその公開鍵で暗号化します。サーバーは暗号文をメモリ上で中継するだけです。
- **Web暗号化の限界：** ブラウザを使う以上、サーバーが配信するJavaScriptを信頼する必要があります。この設計はデータベース・ログ・バックアップの流出を防ぎますが、運営者自身の悪意までは防げません。`SHA256SUMS`で検証したコマンドラインクライアントを使えば、配信コードへの信頼は不要になります。

Flickは一時的な受け渡し用ツールであり、パスワード管理ツールや長期保管庫ではありません。脅威モデル全体とフォーマットは[security-model.md](docs/architecture/security-model.md)と[age-format-v1.md](contracts/age-format-v1.md)にまとめています。

## コマンドラインクライアント（`flick`）

`flick`を使うと、ブラウザと同じローカル暗号化方式でターミナルから送信・開封できます。

### インストール

macOS・Linux・Windows向けのビルド済みバイナリが、各`cli/v*`[リリース](https://github.com/Felix-LeeSM/flick-drop/releases)に`SHA256SUMS`と一緒に添付されています。実行前に必ず検証してください。

**macOS / Linux：**

```bash
version=v0.2.0
platform=darwin_arm64   # または darwin_amd64, linux_amd64, linux_arm64
base=https://github.com/Felix-LeeSM/flick-drop/releases/download/cli/$version

curl -fsSLO "$base/flick_${version}_${platform}.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
shasum -a 256 --ignore-missing -c SHA256SUMS   # Linuxでは sha256sum -c

tar -xzf "flick_${version}_${platform}.tar.gz"
sudo install -m 0755 flick /usr/local/bin/flick
flick version
```

**Windows：** 同じリリースから`flick_<version>_windows_amd64.tar.gz`と`SHA256SUMS`をダウンロードし、`Get-FileHash flick_<version>_windows_amd64.tar.gz -Algorithm SHA256`で検証したあと、`flick.exe`を`PATH`の通った場所へ配置してください。

**ソースからビルド（Go 1.25以降）：**

```bash
go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
```

> `cli/v*`タグはGoモジュールバージョンではないため、バージョンを固定したい場合はリリースページに記載されたコミットハッシュを指定してください。

### 使い方

```bash
# 接続先のFlickインスタンスを指定（以下はデモ環境。セルフホスト時は自分のオリジンに変更）
export FLICK_URL=https://flick.makana.cc

# パスワードなしで送信（鍵は #key=... に含まれる）
flick send "sk-live-example"
kubectl get secret db -o jsonpath='{.data.password}' | base64 -d | flick send

# パスワード保護やファイル送信
flick send -passphrase -ttl 24h "the database password"
flick send -file ./credentials.json -ttl 30m

# 対話形式で入力して送信
flick send

# 開封（開くとサーバーから削除される）
flick open 'https://flick.makana.cc/s/abc123#key=...'
flick open https://flick.makana.cc/s/abc123 > recovered.txt
```

フラグはテキストやリンクの前後のどちらにも書けます。コマンド引数は`ps`やシェル履歴に残るため、パスワードはフラグでは受け付けません。スクリプトから実行する場合は、入力プロンプトの代わりに環境変数`FLICK_PASSPHRASE`を使ってください。

## セルフホスト

Flickはマネージドデータベースを使わず、小規模なKubernetesクラスターやOCI Always Freeリソースで動かせるように設計されています。OCIのクォータやAlways Freeのポリシーは変わることがあるため、自分のテナンシーの現在の上限を確認してください。

- **必須構成：** `flick-web`（ビルド済みSvelteKitアプリを配信するnginx）、`flick-api`（Go HTTP API）、`flick-worker`（Goクリーンアップワーカー）、`nats`（NATS JetStream）、PostgreSQL 17（`flick_api`・`flick_worker`データベース）。
- **任意構成：** Valkey（リアルタイム転送を有効にする場合は必須）、S3互換オブジェクトストレージ（使わない場合、ファイルサイズはDBインライン上限の1 MiBまで）。
- **コンテナイメージ：** `docker.io/haradwaith/flick-api`、`flick-worker`、`flick-web`として公開されており、バージョンまたはコミットのタグが付きます（`latest`タグはありません）。
- **マニフェストと設定：** Kubernetesマニフェストは[`deploy/base`](deploy/base)にあります。[k3s手順書](docs/runbook/k3s-base.md)や[OCI Always Free手順書](docs/runbook/oci-free-tier.md)を参照し、設定項目は[`.env.example`](.env.example)と[env-contract.md](docs/architecture/env-contract.md)を確認してください。

## リポジトリの範囲

このリポジトリは公開リポジトリです。ソースコード、Dockerfile、汎用のKubernetesマニフェスト、環境変数の例、ドキュメントだけを置きます。認証情報、kubeconfig、秘密鍵、管理者トークン、本番ドメイン（上記のデモを除く）、実際のバケット名、データベース・PVC・バックアップのダンプは、非公開の運用リポジトリかローカルのオーバーレイに置いてください。

## ローカル開発

ツールバージョンは`mise`で固定しています。

```sh
mise install
mise trust
direnv allow
```

`.env.example`をもとに`.env.local`を作成し（`.envrc`が自動で読み込みます。実際の認証情報はコミットしないでください）、開発スタックを起動します。

```sh
mise run dev
```

主なチェックコマンド：

```sh
mise run check       # ローカルCIのエントリポイント
mise run smoke-nats  # NATSスモークテスト
mise run images      # ローカルコンテナイメージのビルド
mise run manifests   # Kubernetesマニフェストの検証
mise run smoke-k3d   # ローカルk3dデプロイのスモークテスト（任意）
```

> Docker・k3d・kubectl・Nodeなどのローカルツールが未導入の項目は、失敗せず`skipped`と表示されます。全項目通過とみなす前に出力を確認してください。

## ドキュメント（英語）

**アーキテクチャ**
- [Service topology](docs/architecture/service-topology.md) · [Security model](docs/architecture/security-model.md) · [Storage model](docs/architecture/storage-model.md) · [Database schema](docs/architecture/database-schema.md)
- [Event contract](docs/architecture/event-contract.md) · [Request-link contract](docs/architecture/request-links.md) · [Environment contract](docs/architecture/env-contract.md)
- [Deployment target](docs/architecture/deployment-target.md) · [Implementation choices](docs/architecture/implementation-choices.md) · [CI and testing](docs/architecture/ci-testing.md) · [Agent workflow](docs/architecture/agent-workflow.md)

**運用手順書・ロードマップ**
- [Local development](docs/runbook/local-dev.md) · [Container images](docs/runbook/container-images.md) · [k3s base manifests](docs/runbook/k3s-base.md) · [k3d smoke](docs/runbook/k3d-smoke.md) · [OCI Free Tier deployment](docs/runbook/oci-free-tier.md) · [Roadmap](docs/ROADMAP.md)

## ライセンス

Flickは[Apache License 2.0](LICENSE)のもとで公開されています。
