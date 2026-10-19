#!/bin/sh
# sshd's AuthorizedKeysCommand: the keys Skybreaker wrote, as they are now,
# so a key removed in the UI stops working at once.
exec cat /etc/skybreaker/authorized_keys 2>/dev/null
