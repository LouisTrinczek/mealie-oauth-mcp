#!/bin/sh
set -eu

profile=$1
thresholds=$2
module=$(go list -m)

awk -v module="$module" -v thresholds="$thresholds" '
BEGIN {
	while ((getline line < thresholds) > 0) {
		split(line, fields, " ")
		if (fields[1] != "") {
			minimum[fields[1]] = fields[2]
		}
	}
	close(thresholds)
}
NR > 1 {
	file = $1
	sub(/:.*/, "", file)
	sub("^" module "/?", "", file)
	slash = match(file, /\/[^\/]*$/)
	pkg = slash ? substr(file, 1, slash - 1) : "."
	total[pkg] += $2
	if ($3 > 0) {
		covered[pkg] += $2
	}
}
END {
	if (length(total) == 0) {
		print "no packages found in coverage profile" > "/dev/stderr"
		exit 1
	}
	failed = 0
	for (pkg in total) {
		required = (pkg in minimum) ? minimum[pkg] : minimum["default"]
		percent = 100 * covered[pkg] / total[pkg]
		printf "%s %.1f%% %s%%\n", pkg, percent, required
		if (percent < required) {
			failed = 1
		}
	}
	exit failed
}
' "$profile"
