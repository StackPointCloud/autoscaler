

### build and release
```
$ VERSION=1.1.0.b
$ git tag stackpointio-${VERSION}
$ git push -f --tags
$ REGISTRY=quay.io/stackpoint TAG=stackpointio-${VERSION} make execute-release
```