#!/bin/sh
# AIStudio2API 容器入口：
# Build App 需要「有头」Camoufox（OS 级真实鼠标点击建立 user activation），
# 因此在容器内启动 Xvfb 虚拟屏（DISPLAY=:99）供 Camoufox 显示。
set -e
if [ ! -e /tmp/.X11-unix/X99 ]; then
  Xvfb :99 -screen 0 1680x1050x24 -nolisten tcp &
  sleep 1
fi
export DISPLAY=:99
export BUILDAPP_HEADLESS=${BUILDAPP_HEADLESS:-false}
exec ./aistudio2api "$@"
