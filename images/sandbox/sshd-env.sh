#!/bin/sh
# Prints this environment as one sshd SetEnv line (sshd keeps only the
# first it reads), so SSH sessions get what `docker run` gave the container:
# the broker proxy and the rest. NULs separate the variables and a newline
# inside a value becomes \001, so a value can't start a line of its own; any
# value with a control character, quote or backslash is dropped rather than
# quoted.
env -0 | tr '\n\000' '\001\n' | awk '
  /^[A-Za-z_][A-Za-z0-9_]*=/ {
    i = index($0, "="); k = substr($0, 1, i - 1); v = substr($0, i + 1)
    if (k ~ /^(HOME|HOSTNAME|PWD|OLDPWD|SHLVL|TERM|USER|LOGNAME|SHELL|MAIL|_)$/) next
    if (v ~ /[[:cntrl:]"\\]/) next
    line = line " " k "=\"" v "\""
  }
  END { if (line != "") print "SetEnv" line }'
