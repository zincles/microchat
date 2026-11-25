#!/bin/sh
# 更新 public/favicon.svg 后重新生成安装图标；部署只使用提交的 PNG，不依赖 ImageMagick。
set -eu
cd "$(dirname "$0")/.."
out=src/assets/app-icons
mkdir -p "$out"

magick -background none -density 768 public/favicon.svg -resize 512x512 -strip "PNG32:$out/pwa-512.png"
magick "$out/pwa-512.png" -resize 192x192 -strip "PNG32:$out/pwa-192.png"
# 以 SVG 的底色铺满画布，给 Android 遮罩的安全区留边。
matte=$(magick "$out/pwa-512.png" -format '%[pixel:p{256,32}]' info:)
magick "$out/pwa-512.png" -resize 416x416 -background "$matte" -gravity center -extent 512x512 \
  -alpha remove -alpha off -strip "PNG24:$out/pwa-maskable-512.png"
magick "$out/pwa-512.png" -resize 180x180 -background "$matte" \
  -alpha remove -alpha off -strip "PNG24:$out/apple-touch-icon.png"
