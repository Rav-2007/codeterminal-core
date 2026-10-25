# Contributing to Mochiii

Thank you for helping. This page covers the two things every contribution needs:
the licence it is made under, and a sign-off on each commit.

For how the code is laid out and how to work on it, start with
[`docs/HANDOFF.md`](docs/HANDOFF.md). Before you open a pull request, run
`make check`, the local gate suite.

## Your contribution is licensed under Apache-2.0

Mochiii is licensed under the [Apache License, Version 2.0](LICENSE). By
submitting a contribution, you agree that it is licensed under the same terms,
as section 5 of the licence describes. There is no separate contributor licence
agreement. The sign-off below is the only other thing you are asked for.

Third-party code keeps its own licence. If your change adds a dependency, or
copies code from another project, add its licence text to
[`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt) and say so in the pull
request.

## Sign off every commit (DCO 1.1)

Each commit must end with a `Signed-off-by:` line that carries your name and
the same email as the commit's author:

```
Signed-off-by: Your Name <you@example.com>
```

By adding that line, you certify the Developer Certificate of Origin 1.1, which
is reproduced in full below. `git commit -s` adds the line for you. To sign off
commits already on your branch, run `git rebase --signoff main` and then
force-push the branch.

A sign-off is a statement you make yourself. Nobody can add one on your behalf.

[`scripts/dco.sh`](scripts/dco.sh) checks the commits a pull request adds, and
[`.github/workflows/dco.yml`](.github/workflows/dco.yml) runs it on every pull
request to `main`. For now it reports an unsigned commit but does not block the
merge. When it becomes a required check, a pull request with an unsigned commit
cannot be merged.

### Developer Certificate of Origin

The text below is reproduced verbatim from <https://developercertificate.org/>.

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```
