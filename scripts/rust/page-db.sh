#!/usr/bin/env bash
# page-db.sh create|drop <n>: one database for each doc page (make rs-doctest, ADR 0021 §11).
# <n> is digits and underscores (rs-doctest passes <shell pid>_<page number>).
#   create  creates spinup_doc_<n> on the server of DATABASE_URL and prints its URL.
#   drop    drops spinup_doc_<n> (WITH (FORCE)).
# Two pages that migrate one database fail, because sqlx finds versions that the page does
# not have. The caller captures the URL in a variable: the script never logs the password.
set -euo pipefail
if [[ $# -ne 2 || ! $2 =~ ^[0-9_]+$ ]]; then
  echo "usage: page-db.sh create|drop <n>" >&2
  exit 2
fi
: "${DATABASE_URL:?page-db: DATABASE_URL is empty}"
name=spinup_doc_$2
export PGOPTIONS="-c client_min_messages=warning"
case $1 in
  create)
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "DROP DATABASE IF EXISTS $name WITH (FORCE)" > /dev/null
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE $name" > /dev/null
    # Replace the last path segment and keep the query.
    base=${DATABASE_URL%%\?*}
    query=
    [[ $DATABASE_URL == *\?* ]] && query="?${DATABASE_URL#*\?}"
    scheme=${base%%://*}
    rest=${base#*://}
    if [[ $rest == */* ]]; then
      rest=${rest%/*}
    fi
    printf '%s://%s/%s%s\n' "$scheme" "$rest" "$name" "$query"
    ;;
  drop)
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -c "DROP DATABASE IF EXISTS $name WITH (FORCE)" > /dev/null
    ;;
  *)
    echo "usage: page-db.sh create|drop <n>" >&2
    exit 2
    ;;
esac
