function trim(value) {
  sub(/^[[:space:]]*/, "", value)
  sub(/[[:space:]]*$/, "", value)
  return value
}

function is_required_reject(line, remainder) {
  if (index(line, prefix) != 1) {
    return 0
  }
  remainder = substr(line, length(prefix) + 1)
  return remainder ~ /^[[:space:]]+reject([[:space:]]+with[[:space:]].+)?$/
}

{
  line = trim($0)
}

line == "chain " chain " {" {
  in_chain = 1
  next
}

in_chain && line == "}" {
  in_chain = 0
  next
}

in_chain && is_required_reject(line) {
  found = 1
}

END {
  exit(found ? 0 : 1)
}
