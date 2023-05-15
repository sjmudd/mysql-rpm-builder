#!/bin/sh
#
# build environment for to build MySQL
# - build the builduser part, sourced from build-environment.sh

build () {
	SRPMS="https://yum.oracle.com/repo/OracleLinux/OL8/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.32-1.el8.src.rpm"

	# Install the appropriate src.rpm from upstream sources
	# - keep local copy to speed up process is we do this frequently
	for url in $SRPMS; do
		rpm=$(basename $url)
		if [ ! -e /data/SRPMS/$rpm ]; then
			echo "Downloading $url to /data/SRPMS"
			( cd /data/SRPMS && wget $url )
		fi
		location=/data/SRPMS/$rpm
		echo "Installing $url from $location"
	done

	# build package
	# - FIXME (add signing)
	# - commercial 0 should not be needed as should be default (?)
	cd ~/rpmbuild/SPECS
	rpmbuild --define 'commercial 0' --define 'el8 1' --define 'rhel 8' -ba mysql.spec
}
