#!/bin/sh
set -eu

dayorder-pgbackrest --stanza=dayorder stanza-create
dayorder-pgbackrest --stanza=dayorder check
