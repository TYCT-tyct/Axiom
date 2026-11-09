# A deployment's environment file read and edited the way Compose reads it, for the host scripts that check or change
# one (up.sh, restore.sh): one reader, so that a setting those two see differently cannot exist. Sourced, never run.
#
# An assignment is NAME=VALUE with an optional `export `, spaces before the name and around the '=', and a CR at the
# end; the last assignment of a name wins. A quoted value ends at its closing quote, an unquoted one at a space before
# '#'; empty is unset to the program. Only AXIOM_* names: the files carry other settings no script here reads.

# env_assignments FILE: every AXIOM_* assignment in FILE, in order, as NAME, a tab and the value.
env_assignments() {
  local line value
  while IFS= read -r line || [ -n "$line" ]; do
    [[ ${line%$'\r'} =~ ^[[:space:]]*(export[[:space:]]+)?(AXIOM_[A-Za-z0-9_]+)[[:space:]]*=[[:space:]]*(.*)$ ]] || continue
    value=${BASH_REMATCH[3]}
    case $value in
    \"*) value=${value#\"} value=${value%%\"*} ;;
    \'*) value=${value#\'} value=${value%%\'*} ;;
    \#*) value= ;;
    *) value=${value%%[[:space:]]#*} value=${value%"${value##*[![:space:]]}"} ;;
    esac
    printf '%s\t%s\n' "${BASH_REMATCH[2]}" "$value"
  done < "$1"
}

# env_value FILE NAME: the value FILE gives NAME, empty when it gives none.
env_value() {
  local name value found=
  while IFS=$'\t' read -r name value; do
    [ "$name" != "$2" ] || found=$value
  done < <(env_assignments "$1")
  printf '%s' "$found"
}

# env_clear FILE NAME...: every assignment of each NAME goes, in whichever form it was written, and FILE ends with an
# empty NAME= for each: the setting is empty however the file spelled it, and one line is left to edit by hand.
env_clear() {
  local file=$1 name
  shift
  for name in "$@"; do
    sed -i -E "/^[[:space:]]*(export[[:space:]]+)?$name[[:space:]]*=/d" "$file"
  done
  [ ! -s "$file" ] || [ -z "$(tail -c1 "$file")" ] || printf '\n' >> "$file"
  for name in "$@"; do printf '%s=\n' "$name" >> "$file"; done
}
