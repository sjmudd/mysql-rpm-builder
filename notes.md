# Various notes

Some notes about things I'm doing or failed builds etc

## things to do:

- start to build / get working CentOS 7 rpms
- start to build / get working CentOS 9 rpms



## Build failure issues

### failure in 8.0.33 / centos 9

centos 9 
- fails if only `rh9` used
- try with `openssl11`

`openssl11-devel` is needed by `mysql-community-8.0.33-1.el9.x86_64`

However, I noticed that by default centos 9 comes with
`openssl-libs-3.0.7-17.el9.x86_64` installed implying that using `--define 'with_openssl 1'` is not appropriate.


### failure in 8.0.26 / CentOS 8

```
Processing files: mysql-community-test-8.0.26-1.el8.x86_64
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_ldap_sasl_client.so
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_kerberos_client.so
Executing(%doc): /bin/sh -e /var/tmp/rpm-tmp.6QYy9b
+ umask 022
+ cd /home/rpmbuild/rpmbuild/BUILD
+ cd mysql-8.0.26
+ DOCDIR=/home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/share/doc/mysql-community-test
+ export LC_ALL=C
+ LC_ALL=C
+ export DOCDIR
+ /usr/bin/mkdir -p /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/share/doc/mysql-community-test
+ cp -pr mysql-8.0.26/LICENSE /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/share/doc/mysql-community-test
+ cp -pr mysql-8.0.26/README /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/share/doc/mysql-community-test
+ exit 0
```

### failure on 8.0.28 / CentOS 8

On a community built 8.0.28 setup I see:

```
$ rpm -q mysql-community-client-plugins
mysql-community-client-plugins-8.0.33-1.el8.x86_64
$ rpm -ql mysql-community-client-plugins
/usr/lib/.build-id
/usr/lib/.build-id/16
/usr/lib/.build-id/16/b69858338878ec3a998a9f6609ff8b8384d56a
/usr/lib/.build-id/25/38e5f6d80d400d3fff4ed341c088544d8c6872
/usr/lib/.build-id/60/8e6ab874310039607ee799817fbc98d2453f63
/usr/lib/.build-id/64
/usr/lib/.build-id/64/511b4fc2341bf08c573786e5616279eda42e69
/usr/lib/.build-id/fb/b73f4e7d8b5d6ae9c38dbd52781c8326ad810b
/usr/lib64/mysql/plugin/authentication_fido_client.so
/usr/lib64/mysql/plugin/authentication_kerberos_client.so
/usr/lib64/mysql/plugin/authentication_ldap_sasl_client.so
/usr/lib64/mysql/plugin/authentication_oci_client.so
/usr/lib64/mysql/private/libfido2.so.1
/usr/lib64/mysql/private/libfido2.so.1.8.0
/usr/share/doc/mysql-community-client-plugins
/usr/share/doc/mysql-community-client-plugins/LICENSE
/usr/share/doc/mysql-community-client-plugins/README
$
```

So the 4 authentication_plugins are GPL and *should be* built.

```
$ docker run -it -v $PWD:/data quay.io/centos/centos:stream8 /data/build -a 8.0.28

....
RPM build errors:
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_ldap_sasl_client.so
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.26-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_kerberos_client.so

Provides: mysql-common = 8.0.28-1.el8 mysql-common(x86-64) = 8.0.28-1.el8 mysql-community-common = 8.0.28-1.el8 mysql-community-common(x86-64) = 8.0.28-1.el8
Requires(rpmlib): rpmlib(CompressedFileNames) <= 3.0.4-1 rpmlib(FileDigests) <= 4.6.0-1 rpmlib(PayloadFilesHavePrefix) <= 4.0-1
Processing files: mysql-community-test-8.0.28-1.el8.x86_64
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_ldap_sasl_client.so
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_fido_client.so
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_kerberos_client.so
error: File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_oci_client.so
Executing(%doc): /bin/sh -e /var/tmp/rpm-tmp.GLgrQR
+ umask 022
+ cd /home/rpmbuild/rpmbuild/BUILD
+ cd mysql-8.0.28
+ DOCDIR=/home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/share/doc/mysql-community-test
+ export LC_ALL=C
+ LC_ALL=C
+ export DOCDIR
+ /usr/bin/mkdir -p /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/share/doc/mysql-community-test
+ cp -pr mysql-8.0.28/LICENSE /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/share/doc/mysql-community-test
+ cp -pr mysql-8.0.28/README /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/share/doc/mysql-community-test
+ exit 0

Summary:

RPM build errors:
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_ldap_sasl_client.so
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_fido_client.so
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_kerberos_client.so
    File not found: /home/rpmbuild/rpmbuild/BUILDROOT/mysql-community-8.0.28-1.el8.x86_64/usr/lib64/mysql/plugin/debug/authentication_oci_client.so
```

- I don't see clear instructions in the mysql.spec on ensuring these packages are built.
- note: this is the `mysql-community-test` rpm where this fails. 
