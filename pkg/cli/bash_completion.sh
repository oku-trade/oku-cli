# Bash completion uses the urfave/cli v3 candidate protocol. Its stock bash
# script evaluates joined user input, so this wrapper passes argv as an array.
__oku_bash_autocomplete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local -a req=("${COMP_WORDS[@]:0:COMP_CWORD}")
  local word opts line token description candidate
  COMPREPLY=()
  for word in "${COMP_WORDS[@]}"; do
    case "$word" in
      *'$('*|*'`'*|*';'*|*'>'*|*'<'*|*'|'*|*'&'*) return 0 ;;
    esac
  done
  if [[ "$cur" == -* ]]; then
    req+=("$cur")
  fi
  opts=$("${req[@]}" --generate-shell-completion 2>/dev/null) || return 0
  local -a tokens=() descriptions=()
  local longest=0
  while IFS= read -r line; do
    token="${line%%:*}"
    [[ -z "$token" ]] && continue
    description=""
    [[ "$line" == *:* ]] && description="${line#*:}"
    tokens+=("$token")
    descriptions+=("$description")
    (( ${#token} > longest )) && longest=${#token}
  done <<< "$opts"
  local index
  for index in "${!tokens[@]}"; do
    candidate="${tokens[index]}"
    [[ "$candidate" == "$cur"* ]] || continue
    if [[ "${COMP_TYPE:-}" == 63 && -n "${descriptions[index]}" ]]; then
      printf -v candidate '%-*s  -- %s' "$longest" "$candidate" "${descriptions[index]}"
    fi
    COMPREPLY+=("$candidate")
  done
}
complete -o bashdefault -o default -F __oku_bash_autocomplete oku
