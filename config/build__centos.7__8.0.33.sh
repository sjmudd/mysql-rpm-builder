#!/bin/sh

build () {
    SRPMS="http://repo.mysql.com/yum/mysql-8.0-community/el/7/SRPMS/mysql-community-8.0.33-1.el7.src.rpm"
    install_srpms "$SRPMS"
    install_custom_patches $mysql_build_version
    rpmbuild_rpms
}
