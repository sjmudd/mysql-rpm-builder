#!/bin/sh
#
# build environment for to build MySQL
# - build rpms, sourced from build-environment.sh

build () {
    SRPMS="https://yum.oracle.com/repo/OracleLinux/OL9/MySQL80/community/x86_64/getPackageSource/mysql-community-8.0.33-1.el9.src.rpm"
    install_srpms "$SRPMS"
    rpmbuild_rpms
}
