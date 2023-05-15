#!/bin/sh
#
# build environment for to build MySQL
# - build the builduser part, sourced from build-environment.sh

build () {
    # install the appropriate src.rpm directly from source
	rpm -ivh https://yum.oracle.com/repo/OracleLinux/OL8/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.26-1.el8.src.rpm

	# no modifications / patches to apply here

	# build package
	# - FIXME (add signing)
    # - commercial 0 should not be needed as should be default (?)
	cd ~/rpmbuild/SPECS
	rpmbuild --define 'commercial 0' --define 'el8 1' --define 'rhel 8' -ba mysql.spec
}
