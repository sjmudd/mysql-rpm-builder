#!/bin/sh

set -e

prepare() {
	cd /data

	yum update -y
	yum install -y 'dnf-command(config-manager)'

	# Handle OEL8 differences vs CentOS 8 stream
	if rpm -q oraclelinux-release 2>&1 >/dev/null; then
		extra_repo=ol8_codeready_builder
	else
		extra_repo=powertools
	fi
	echo "### Enabling extra repo: $extra_repo"
	yum config-manager --set-enabled $extra_repo

	echo "### Installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake \
		cyrus-sasl-devel libaio-devel \
		gcc-toolset-11 \
		gcc-toolset-11-annobin-annocheck \
		gcc-toolset-11-annobin-plugin-gcc \
		git \
		libcurl-devel \
		libtirpc-devel \
		libudev-devel \
		ncurses-devel \
		numactl-devel \
		openldap-devel \
		openssl-devel \
		perl \
		perl-JSON rpcgen \
		rpm-build \
		time \
		wget
	# patch gcc-toolset to avoid build problems
	if ! [ -e /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/gcc-annobin.so ]; then
		echo "### Symlinking gcc-annobin.so to annobin.so"
		(
			cd /opt/rh/gcc-toolset-11/root/usr/lib/gcc/x86_64-redhat-linux/11/plugin/ && \
			ln -s annobin.so gcc-annobin.so
		)
	else
		echo "### Symlink gcc-annobin.so already exists"
	fi

	# ensure gcc-toolset-11 is enabled when building
	if ! grep /opt/rh/gcc-toolset-11/enable /etc/bashrc; then
		echo "### Patching /etc/bashrc to enable gcc-toolset-11"
		echo "source /opt/rh/gcc-toolset-11/enable" >> /etc/bashrc
	else
		echo "### /etc/bashrc already patched to enable gcc-toolset-11"
	fi
	echo "########################################################"
	echo "#           os preparation complete                    #"
	echo "########################################################"
}
