CGO_LDFLAGS_ALLOW='-Wl,--unresolved-symbols=ignore-in-object-files' go build -buildvcs=false -gcflags="all=-N -l" -o ../tools/dra-example-kubeletplugin ../../cmd/dra-example-kubeletplugin
