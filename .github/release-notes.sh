#!/bin/sh
# Prints the CHANGELOG.md section for version $1 (without the leading "v").
awk -v v="$1" '/^## \[/{p = (index($0, "[" v "]") > 0); next} p && !/^\[[^]]+\]: /' CHANGELOG.md > /tmp/notes.$$
[ -s /tmp/notes.$$ ] && cat /tmp/notes.$$ || echo "See CHANGELOG.md"
rm -f /tmp/notes.$$
