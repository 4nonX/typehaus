#!/bin/sh
# Quit running typehaus apps when the package is removed (not on upgrade).
# Fonts stay installed per user; run "typehaus unenroll" before removing to
# take them off too.
case "$1" in
  remove|purge|0) pkill -x typehaus 2>/dev/null || true ;;
esac
exit 0
