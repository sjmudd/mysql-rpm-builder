#!/bin/sh
#
# build environment for to build MySQL
# - build the builduser part, sourced from build-environment.sh

build () {
    SRC_RPMS="https://yum.oracle.com/repo/OracleLinux/OL8/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.26-1.el8.src.rpm"

    install_srpms "$SRC_RPMS"
    rpmbuild_rpms
}
