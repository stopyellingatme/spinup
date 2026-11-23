#!/bin/sh
# The collector implementation replaces this explicit failure stub.
printf '%s\n' 'Cannot collect console facts: the collector is unavailable.' 'Try: upgrade the CLI to a version with the console collector.' >&2
exit 1
