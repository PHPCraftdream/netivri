#!/usr/bin/env bash
# Decides how much of the test suite a CI run executes and prints it as
# key=value lines for $GITHUB_OUTPUT:
#   packages=...        Go packages to test: "./..." for everything, a list of
#                       ./dir patterns, or empty when nothing needs a test
#   full=true|false     true when everything runs (coverage floors, npm and
#                       linux-static checks run only then)
#   run=                -run regex (manual runs only)
#   os=all              all, ubuntu, windows or macos (manual runs only)
#   matrix_os=[...]     runners of the unit and integration jobs (JSON)
#   matrix_os_e2e=[...] runners of the e2e job (JSON; there is no macOS e2e)
#   note=...            one line for the job summary
#
# A push or pull request runs the packages the diff touches and every package
# that imports them (tests included). A change that is not a file of one Go
# package (go.mod, workflows, scripts, third_party, npm, packaging, ...) runs
# everything, and a change of documents alone runs no test. A line
# "ci: full" in the message of the head commit runs everything too: that is
# the only thing the message can ask for. A manual run (workflow_dispatch)
# takes its inputs, which default to everything.
#
# Inputs (environment):
#   CI_EVENT       github.event_name
#   CI_MESSAGE     message of the head commit (untrusted: only "ci: full" is read)
#   CI_CHANGED     files of the diff, one per line; unset when the diff is
#                  unknown (everything runs)
#   CI_MODULE      the Go module path
#   CI_PKGS        import paths of every package of the module, one per line
#   CI_GRAPH       `go list -test -f '{{.ImportPath}}<TAB>{{join .Deps " "}}'`
#   CI_CHANGED_FILE, CI_PKGS_FILE, CI_GRAPH_FILE
#                  the same three as files (an environment has a size limit,
#                  the graph of the module's dependencies exceeds it)
#   CI_OWNERS      file of "path-prefix ./package" lines: files that a test of
#                  another package reads (default scripts/ci-scope-owners.txt)
#   CI_IN_PACKAGES, CI_IN_RUN, CI_IN_OS   workflow_dispatch inputs
set -euo pipefail
set -f # no globbing: every value is data

event="${CI_EVENT:-}"

packages="./..."
run=""
os="all"
full=true
note="full run"

fail() {
  echo "ci-scope: $*" >&2
  exit 1
}

# A package pattern is ./..., or ./dir[/dir...] with an optional /... .
re_all='^\./\.\.\.$'
re_package='^\./[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*(/\.\.\.)?$'
valid_package() {
  [[ "$1" =~ ${re_all} || "$1" =~ ${re_package} ]]
}

# A -run regex: letters, digits and the usual regex punctuation, no spaces,
# quotes, backslashes or shell metacharacters.
re_run='^[]A-Za-z0-9_|^$.()*+?[-]+$'
valid_run() {
  [[ ${#1} -le 200 && "$1" =~ ${re_run} && "$1" != *'$('* ]]
}

re_os='^(all|ubuntu|windows|macos)$'

dispatch() {
  local value="${CI_IN_PACKAGES:-./...}" token list=() count=0
  for token in ${value}; do
    valid_package "${token}" || fail "bad package pattern '${token}' (want ./dir/... or ./dir)"
    list+=("${token}")
    count=$((count + 1))
  done
  [[ ${count} -gt 0 && ${count} -le 20 ]] || fail "want 1 to 20 package patterns, got ${count}"
  packages="${list[*]}"
  if [[ -n "${CI_IN_RUN:-}" ]]; then
    valid_run "${CI_IN_RUN}" || fail "bad -run regex '${CI_IN_RUN}'"
    run="${CI_IN_RUN}"
  fi
  if [[ -n "${CI_IN_OS:-}" ]]; then
    [[ "${CI_IN_OS}" =~ ${re_os} ]] || fail "bad os '${CI_IN_OS}' (want all, ubuntu, windows or macos)"
    os="${CI_IN_OS}"
  fi
  note="manual run"
  if [[ "${packages}" != "./..." || -n "${run}" || "${os}" != "all" ]]; then
    full=false
    note="manual scoped run: packages ${packages}${run:+, -run ${run}}, os ${os}"
  fi
}

# asked_full tells whether the commit message has a line "ci: full" (a bullet
# or indentation before it is fine, the case is not).
re_full='^[[:space:]*-]*ci:[[:space:]]*full[[:space:]]*$'
asked_full() {
  local line
  while IFS= read -r line; do
    line="${line%$'\r'}"
    if [[ "${line,,}" =~ ${re_full} ]]; then
      return 0
    fi
  done <<< "${CI_MESSAGE:-}"
  return 1
}

# diff_scope narrows packages to what the diff touches.
diff_scope() {
  if asked_full; then
    note="full run (the commit message says ci: full)"
    return
  fi
  local changed_text="" pkgs_text="${CI_PKGS:-}" graph_text="${CI_GRAPH:-}" have_diff=""
  if [[ -n "${CI_CHANGED_FILE:-}" && -f "${CI_CHANGED_FILE}" ]]; then
    changed_text="$(< "${CI_CHANGED_FILE}")"
    have_diff=1
  elif [[ -n "${CI_CHANGED+x}" ]]; then
    changed_text="${CI_CHANGED}"
    have_diff=1
  fi
  if [[ -z "${have_diff}" ]]; then
    note="full run (the diff is unknown)"
    return
  fi
  if [[ -n "${CI_PKGS_FILE:-}" ]]; then
    pkgs_text="$(< "${CI_PKGS_FILE}")"
  fi
  if [[ -n "${CI_GRAPH_FILE:-}" ]]; then
    graph_text="$(< "${CI_GRAPH_FILE}")"
  fi
  local module="${CI_MODULE:?CI_MODULE}" owners="${CI_OWNERS:-scripts/ci-scope-owners.txt}"
  local -A dir_of=() known=() changed=() selected=()
  local path rel total=0 f d best pkg other="" prefix owner name deps dep

  while IFS= read -r path; do
    [[ -z "${path}" ]] && continue
    known["${path}"]=1
    total=$((total + 1))
    rel="${path#"${module}"}"
    rel="${rel#/}"
    if [[ -n "${rel}" ]]; then
      dir_of["${rel}"]="${path}"
    fi
  done <<< "${pkgs_text}"
  [[ ${total} -gt 0 ]] || fail "CI_PKGS is empty"

  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    # A file that a test of some package reads belongs to that package.
    owner=""
    if [[ -f "${owners}" ]]; then
      while read -r prefix pkg; do
        if [[ -z "${prefix}" || "${prefix}" == \#* ]]; then
          continue
        fi
        if [[ "${f}" == "${prefix}" || "${f}" == "${prefix}"/* ]]; then
          owner="${module}/${pkg#./}"
        fi
      done < "${owners}"
    fi
    if [[ -n "${owner}" ]]; then
      changed["${owner}"]=1
      continue
    fi
    # Anything below the directory of a package belongs to the innermost one.
    best=""
    for d in "${!dir_of[@]}"; do
      if [[ "${f}" == "${d}"/* && ${#d} -gt ${#best} ]]; then
        best="${d}"
      fi
    done
    if [[ -n "${best}" ]]; then
      changed["${dir_of[${best}]}"]=1
      continue
    fi
    # Documents are not tested; anything else may change what every package
    # builds against.
    case "${f}" in
      *.md | docs/* | LICENSE) ;;
      *) other="${f}" ;;
    esac
  done <<< "${changed_text}"

  if [[ -n "${other}" ]]; then
    note="full run (${other} is not a file of one package)"
    return
  fi

  for pkg in "${!changed[@]}"; do
    selected["${pkg}"]=1
  done
  # A package whose dependencies (its tests' too) include a changed one is
  # selected as well; `go list -test` names those builds "pkg [pkg.test]",
  # "pkg_test [pkg.test]" and "pkg.test".
  while IFS=$'\t' read -r name deps; do
    [[ -z "${name}" ]] && continue
    name="${name%% *}"
    name="${name%.test}"
    name="${name%_test}"
    [[ -n "${known[${name}]:-}" ]] || continue
    for dep in ${deps}; do
      if [[ -n "${changed[${dep}]:-}" ]]; then
        selected["${name}"]=1
        break
      fi
    done
  done <<< "${graph_text}"

  if [[ ${#selected[@]} -eq ${total} ]]; then
    note="full run (the diff reaches all ${total} packages)"
    return
  fi
  full=false
  local list=()
  for pkg in "${!selected[@]}"; do
    rel="${pkg#"${module}"}"
    list+=("./${rel#/}")
  done
  if [[ ${#list[@]} -eq 0 ]]; then
    packages=""
    note="no package to test (documents only)"
    return
  fi
  mapfile -t list < <(printf '%s\n' "${list[@]}" | sort)
  packages="${list[*]}"
  note="scoped run: ${#list[@]} of ${total} packages (${packages}); a ci: full line in the commit message or a manual run tests everything"
}

case "${event}" in
  workflow_dispatch) dispatch ;;
  push | pull_request) diff_scope ;;
  *) ;;
esac

if [[ "${os}" == "all" ]]; then
  matrix_os='["ubuntu-latest","windows-latest","macos-latest"]'
  matrix_os_e2e='["ubuntu-latest","windows-latest"]'
else
  matrix_os="[\"${os}-latest\"]"
  matrix_os_e2e="[\"${os}-latest\"]"
  if [[ "${os}" == "macos" ]]; then
    matrix_os_e2e='[]'
  fi
fi

echo "packages=${packages}"
echo "run=${run}"
echo "os=${os}"
echo "full=${full}"
echo "matrix_os=${matrix_os}"
echo "matrix_os_e2e=${matrix_os_e2e}"
echo "note=${note}"
