#!/bin/sh

build () {
	SRPMS=$(get_srpm_location)
	install_srpms "$SRPMS"
	rpmbuild_rpms
}
