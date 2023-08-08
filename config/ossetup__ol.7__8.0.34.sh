#!/bin/sh


prepare() {
	local rc
	# catch failures and report
	prepare_stage2

	rc=$?

	if [ $rc != 0 ]; then
		echo "########################################################"
		echo "#           os preparation failed                      #"
		echo "########################################################"
	fi

	return $rc
}

prepare_stage2 () {
	set -e

	cd /data

	yum update -y

	# Handle OEL7 differences vs CentOS 7
	if rpm -q oraclelinux-release 2>&1 >/dev/null; then

		yum-config-manager --enable ol7_codeready_builder
		yum-config-manager --enable ol7_developer_EPEL

#		extra_repo=ol7_codeready_builder
#		echo "### Enabling extra repo: $extra_repo"
#		yum config-manager --set-enabled $extra_repo
	else
		echo "ERROR: not running under oraclelinux 7"
		exit 1
#		# Centos only
#		yum install -y centos-release-scl epel-release
	fi

	echo "### Installing required rpms"
	required_rpms="
		bind-utils
		bison
		cmake3
		cyrus-sasl-devel libaio-devel
		devtoolset-11-binutils
		devtoolset-11-gcc
		devtoolset-11-gcc-c++
		git
		libcurl-devel
		libtirpc-devel
		libudev-devel
		ncurses-devel
		numactl-devel
		openldap-devel
		openssl-devel
		perl
		perl-Data-Dumper
		perl-Env
		perl-JSON
		rpcgen 
		rpm-build
		time
		wget"

	yum install -y $required_rpms

	# yum install is silent if some rpms are missing (at least on centos 7)
	# so verify that the required rpms are actually installed as needed.
	missing=
	for rpm in $required_rpms; do
		rpm -q $rpm || missing="$missing $rpm"
	done
	if [ -n "$missing" ]; then
		echo "ERROR: The following rpms are missing: $missing"
		exit 1
	fi

	# ensure devtoolset-11 is enabled when building
	if ! grep /opt/rh/devtoolset-11/enable /etc/bashrc; then
		echo "### Patching /etc/bashrc to enable devtoolset-11"
		echo "source /opt/rh/devtoolset-11/enable" >> /etc/bashrc
	else
		echo "### /etc/bashrc already patched to enable devtoolset-11"
	fi

	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
