#!/bin/sh
#
# build environment for building MySQL from src.rpms
#

setup_build_user () {}
	echo "########################################################"
	echo "#            Preparing OS for building rpms            #"
	echo "########################################################"
	if ! grep $BUILD_USER /etc/passwd; then
		echo "### Adding missing build user $BUILD_USER"
		useradd --no-create-home -d /data $BUILD_USER
	else
		echo "### required build user $BUILD_USER already present"
	fi
}

# Install the appropriate src.rpm from upstream sources
# - keep local copy to speed up process so if we do this frequently
install_srpms () {
	local SRPMS="$1" # space separated list of src.rpms to install from urls

	for url in $SRPMS; do
		rpm=$(basename $url)
		if [ ! -e /data/SRPMS/$rpm ]; then
			echo "Downloading $url to /data/SRPMS"
			( cd /data/SRPMS && wget $url )
		fi
		location=/data/SRPMS/$rpm
		echo "Installing $url from $location"
		rpm -ivh $location
	done
}

# build package
# - FIXME (add signing)
# - FIXME fix config to work with OS other than rhel8 / oel8
rpmbuild_rpms () {
	local timestamp=$(date +%Y%m%d.%H%M%S)
	cd ~/rpmbuild/SPECS
	rpmbuild --define 'el8 1' --define 'rhel 8' -ba mysql.spec 2>&1 | tee -a ~/log/mysql-build-$build_environment.$timestamp.log
	rc=$?

	# If build is successful record the installed package list,
	# or record the failed list as that may need fixing.
	rpm_qa=~/log/rpm-qa.$build_environment.$timestamp
	if [ $rc = 0 ]; then
        rpm -qa | sort > $rpm_qa
    else
        rpm -qa | sort > $rpm_qa.failed
	fi
}

BUILD_USER=rpmbuild

set -e

if [ -z "$USER" ]; then
	USER=$(id -un)
fi

build_environment=$1
if [ -z "$build_environment" ]; then
	echo "please provide build_environment name, directory under config"
	exit 1
fi

case "$USER" in
root)
	##########################
	###    run as root     ###
	##########################
	echo "sourcing prepare script"
	. /data/config/$build_environment/prepare.sh
	prepare
	;;
$BUILD_USER)
	##########################
	### run as $BUILD_USER ###
	##########################
	echo "sourcing perform-build script"
	. /data/config/$build_environment/build.sh 
	build
	;;
*)
	echo "unexpected USER $USER, please call the script properly"
	exit 1
esac
