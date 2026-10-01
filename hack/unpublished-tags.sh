#!/bin/sh
# Prints, on one line and in the order given, the tags of an image that the registry does not have yet. The pipeline
# releases on main with it: it publishes whichever of the chart's appVersion and the commit tag is missing, so a
# commit that does not change the version, or a run that is repeated, publishes nothing twice. Tags are immutable.
#
#   unpublished-tags.sh IMAGE TAG...
#
# Any registry answer other than "not found" is an error, never "unpublished": publishing after a failed lookup
# would move a tag that exists. Needs crane; credentials come from the Docker config.
set -eu

image="${1:?usage: unpublished-tags.sh IMAGE TAG...}"
shift
unpublished=""
for tag in "$@"; do
	if answer="$(crane manifest "${image}:${tag}" 2>&1 >/dev/null)"; then
		continue
	fi
	case "${answer}" in
	*MANIFEST_UNKNOWN* | *NAME_UNKNOWN* | *NOT_FOUND*) unpublished="${unpublished} ${tag}" ;;
	*)
		echo "unpublished-tags.sh: could not look up ${image}:${tag}: ${answer}" >&2
		exit 1
		;;
	esac
done
echo "${unpublished# }"
