#!/usr/bin/env bash
#
# 把一个打好、签好、公证过、钉好章的 DMG 传到 R2.
#
#   **它不打包也不公证** —— 那两步在 docs/RELEASE.md 里, 各自会失败,
#   混成一条命令的下场是"传到一半发现忘了钉章". 这里只管最后一程,
#   而且**传之前会验一遍钉章**: 没钉章的包传上去, 用户断网就打不开.
#
#   **桶是跟别的产品共用的**(Neox 桌面版发在 desktop/ 下), 所以这里的东西
#   一律放在 os/ 前缀下 —— 传到桶根目录迟早会跟别人撞名字.
#
#   依赖 curl 和 openssl(mac 自带), 不装 aws-cli / rclone / wrangler ——
#   为了传一个文件装一套 Python 生态不值得. 用的是 AWS SigV4,
#   R2 认这套协议.
#
# 用法:  tools/publish-dmg.sh <路径/xxx.dmg>
set -euo pipefail

DMG="${1:-}"
[ -n "$DMG" ] && [ -f "$DMG" ] || { echo "用法: $0 <路径/xxx.dmg>" >&2; exit 2; }

for v in R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY R2_BUCKET; do
  [ -n "${!v:-}" ] || { echo "缺环境变量 $v —— 见 docs/RELEASE.md" >&2; exit 2; }
done

# 钉章没钉是这条路上最容易漏的一步, 而它的后果(用户断网打不开)要等到
# 用户装机那天才暴露. 在这儿拦住.
if ! xcrun stapler validate "$DMG" >/dev/null 2>&1; then
  echo "这个 DMG 没钉章 —— 先跑: xcrun stapler staple \"$DMG\"" >&2
  exit 1
fi

# 共用的桶, 各产品各占一个前缀 —— 见文件头
PREFIX="${R2_PREFIX-os}"
KEY="${PREFIX:+$PREFIX/}$(basename "$DMG")"
HOST="${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
NOW="$(date -u +%Y%m%dT%H%M%SZ)"
DAY="${NOW%T*}"
HASH="$(openssl dgst -sha256 -hex "$DMG" | awk '{print $NF}')"

# ── SigV4 ──────────────────────────────────────────────────
# R2 的 region 固定是 auto
CANON="PUT
/${R2_BUCKET}/${KEY}

host:${HOST}
x-amz-content-sha256:${HASH}
x-amz-date:${NOW}

host;x-amz-content-sha256;x-amz-date
${HASH}"
SCOPE="${DAY}/auto/s3/aws4_request"
TOSIGN="AWS4-HMAC-SHA256
${NOW}
${SCOPE}
$(printf '%s' "$CANON" | openssl dgst -sha256 -hex | awk '{print $NF}')"

hmac() { printf '%s' "$2" | openssl dgst -sha256 -mac HMAC -macopt "$1" -hex | awk '{print $NF}'; }
K1=$(hmac "key:AWS4${R2_SECRET_ACCESS_KEY}" "$DAY")
K2=$(hmac "hexkey:$K1" "auto")
K3=$(hmac "hexkey:$K2" "s3")
K4=$(hmac "hexkey:$K3" "aws4_request")
SIG=$(hmac "hexkey:$K4" "$TOSIGN")

echo "传 ${KEY} ($(du -h "$DMG" | cut -f1)) → ${R2_BUCKET} …"
curl --fail-with-body -sS -X PUT "https://${HOST}/${R2_BUCKET}/${KEY}" \
  --upload-file "$DMG" \
  -H "Host: ${HOST}" \
  -H "x-amz-date: ${NOW}" \
  -H "x-amz-content-sha256: ${HASH}" \
  -H "Content-Type: application/x-apple-diskimage" \
  -H "Authorization: AWS4-HMAC-SHA256 Credential=${R2_ACCESS_KEY_ID}/${SCOPE}, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=${SIG}"

echo
echo "传好了。"
if [ -n "${R2_PUBLIC_BASE:-}" ]; then
  echo "下载地址: ${R2_PUBLIC_BASE}/${KEY}"
else
  # r2.dev 是限速的开发地址, 不该拿来当下载页的链接 —— 见 docs/RELEASE.md
  echo "还没设 R2_PUBLIC_BASE —— 配一个自定义域(如 https://dl.neox.dev)再把地址贴到官网。"
fi
echo "sha256: ${HASH}"
