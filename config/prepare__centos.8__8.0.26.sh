#!/bin/sh

set -e

prepare() {
	cd /data
	yum update -y
	yum install -y 'dnf-command(config-manager)'
	yum config-manager --set-enabled powertools

	extras="
		cyrus-sasl-gssapi
		cyrus-sasl-scram
		cyrus-sasl-ldap
	"

	echo "### Installing required rpms"
	yum install -y \
		bind-utils \
		bison \
		cmake \
		cyrus-sasl-devel \
		$extras \
		gcc-toolset-10 \
		git \
		libaio-devel \
		libcurl-devel \
		libtirpc-devel \
		ncurses-devel \
		numactl-devel \
		openldap-devel \
		openssl-devel \
		perl \
		perl-JSON \
		rpcgen \
		rpm-build \
		time \
		wget
	# patch gcc-toolset to avoid build problems
	if ! [ -e /opt/rh/gcc-toolset-10/root/usr/lib/gcc/x86_64-redhat-linux/10/plugin/gcc-annobin.so ]; then
		echo "### Symlinking gcc-annobin.so to annobin.so"
		(
			cd /opt/rh/gcc-toolset-10/root/usr/lib/gcc/x86_64-redhat-linux/10/plugin/ && \
			ln -s annobin.so gcc-annobin.so
		)
	else
		echo "### Symlink gcc-annobin.so already exists"
	fi

	# ensure gcc-toolset-10 is enabled when building
	if ! grep /opt/rh/gcc-toolset-10/enable /etc/bashrc; then
		echo "### Patching /etc/bashrc to enable gcc-toolset-10"
		echo "source /opt/rh/gcc-toolset-10/enable" >> /etc/bashrc
	else
		echo "### /etc/bashrc already patched to enable gcc-toolset-10"
	fi
}
