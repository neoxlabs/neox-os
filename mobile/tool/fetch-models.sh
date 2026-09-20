#!/usr/bin/env bash
#
# fetch-models — 把语音识别模型取到 mobile/assets/ 下.
#
#   **模型不进仓库**: 两个权重加起来 105MB, 而且它们不是本项目的作品 ——
#   随仓分发等于替上游做了一个我们没有资格做的授权声明。仓库里只留
#   取它们的方法和校验和, 拿到的东西是否与我们测过的一致, 由 sha256 说了算。
#
#   两个模型各司其职 (见 mobile/lib/audio/asr.dart):
#     asr/   流式 zipformer   实时字幕, 边说边出字
#     asr2/  离线 paraformer  一句说完后拿整段音频定稿, 准确率更高
#
#   用法: mobile/tool/fetch-models.sh        (已存在且校验通过的跳过)
#         FORCE=1 mobile/tool/fetch-models.sh  (强制重下)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HF="${HF_ENDPOINT:-https://huggingface.co}"

# 目录:仓库:文件:sha256
FILES=(
  "asr:csukuangfj/sherpa-onnx-streaming-zipformer-small-ctc-zh-int8-2025-04-01:model.int8.onnx:68c9c943840f7d9cf3e8a4970ba50f404feb5277f611fa82b7e72267786fa84a"
  "asr:csukuangfj/sherpa-onnx-streaming-zipformer-small-ctc-zh-int8-2025-04-01:tokens.txt:6fed8c6c248516f38e7faa19404b57413e8ce259f1cbc1fa4aebc86eac32fdfd"
  "asr2:csukuangfj/sherpa-onnx-paraformer-zh-small-2024-03-09:model.int8.onnx:3ef6c19369b912f7caf3cef8e545c5ccd1a33d9d7ec792a46668dc41c4b229ec"
  "asr2:csukuangfj/sherpa-onnx-paraformer-zh-small-2024-03-09:tokens.txt:4b2d964e18b9cf139b473003b6698fb2ed9a2a5ec55b93daa677b28f578897aa"
)

sha() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}' || sha256sum "$1" | awk '{print $1}'; }

for entry in "${FILES[@]}"; do
  IFS=: read -r dir repo name want <<< "$entry"
  out="$ROOT/assets/$dir/$name"
  mkdir -p "$(dirname "$out")"

  if [ -f "$out" ] && [ "${FORCE:-}" != "1" ] && [ "$(sha "$out")" = "$want" ]; then
    echo "✓ $dir/$name 已在且校验通过"
    continue
  fi

  echo "↓ $dir/$name  ($repo)"
  curl -fL --progress-bar -o "$out.part" "$HF/$repo/resolve/main/$name"

  got="$(sha "$out.part")"
  if [ "$got" != "$want" ]; then
    rm -f "$out.part"
    echo "✗ $dir/$name 校验失败" >&2
    echo "    期望 $want" >&2
    echo "    实际 $got" >&2
    echo "  上游换了文件, 或者下载被中间人改过。两种都不该静默接受。" >&2
    exit 1
  fi
  mv "$out.part" "$out"
  echo "✓ $dir/$name"
done

echo
echo "模型就位。上游是 k2-fsa/sherpa-onnx 的预训练模型, 许可条款以各自的"
echo "模型仓为准, 本项目不对它们再授权。"
