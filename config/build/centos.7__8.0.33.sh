#!/bin/sh

build () {
    install_srpms $(get_srpm_location)
    install_custom_patches $mysql_build_version
    rpmbuild_rpms
}
