#!/usr/bin/env python3
"""Run a scalar RISC-V test with vector execution disabled for it and its children.

Usage: go test -exec ./scripts/riscv-no-vector.py ...
Build the test for RVA20/RVA22, not a target that requires V. This changes only
the next exec's thread policy, and disables Tango's runtime vector dispatch.
"""

import ctypes
import os
import sys


def main():
    if len(sys.argv) < 2:
        sys.exit("usage: riscv-no-vector.py EXECUTABLE [ARGS...]")
    if sys.platform != "linux" or os.uname().machine != "riscv64":
        sys.exit("requires Linux/riscv64 PR_RISCV_V_SET_CONTROL")

    libc = ctypes.CDLL(None, use_errno=True)
    libc.prctl.argtypes = [ctypes.c_int] + [ctypes.c_ulong] * 4
    libc.prctl.restype = ctypes.c_int
    # PR_RISCV_V_SET_CONTROL: next-exec OFF (1 << 2), inherit (1 << 4).
    # Leave Python's current policy unchanged until exec.
    if libc.prctl(69, 0x14, 0, 0, 0) != 0:
        sys.exit("cannot disable vector execution: " + os.strerror(ctypes.get_errno()))
    debug = os.environ.get("GODEBUG", "")
    os.environ["GODEBUG"] = (debug + "," if debug else "") + "cpu.v=off"
    os.execvpe(sys.argv[1], sys.argv[1:], os.environ)


if __name__ == "__main__":
    main()
