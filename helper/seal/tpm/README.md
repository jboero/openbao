# openbao-seal-tpm

A TPM 2.0 auto-unseal seal wrapper for [OpenBao](https://openbao.org/) (and Vault-compatible forks). Seals the master key to the TPM's Storage Root Key (SRK), with optional PCR binding for measured boot enforcement.

No cloud KMS dependency -- pure local hardware trust for bare-metal and edge deployments.

**By John Boero and Claude -- PoC, not for production use.**

## How It Works

On `openbao init`, the master key is sealed (encrypted) to the local TPM's Storage Root Key using `TPM2_Create`. The sealed blob is stored in OpenBao's physical storage. On startup, `TPM2_Unseal` recovers the master key using the same TPM -- no operator intervention required.

When PCR binding is enabled, the seal is additionally bound to specific Platform Configuration Register values. If boot measurements change (e.g., firmware update, kernel upgrade, bootloader change), the TPM will refuse to unseal until you re-seal with the new PCR values. This ensures the server only auto-unseals when booted into a known-good state.

### Key Properties

- **Hardware-bound**: The sealed key never leaves the TPM. It cannot be extracted or migrated to another machine.
- **SRK-based**: Uses the TCG reference RSA-2048 SRK template. The SRK is deterministic -- recreating it with the same template on the same TPM yields the same key.
- **PCR binding (optional)**: Ties unseal to specific boot measurements. Prevents unseal after unauthorized firmware/OS changes.
- **No network**: Everything is local. No KMS API calls, no cloud credentials, no network dependency at boot.

## Requirements

- TPM 2.0 hardware (or firmware TPM)
- Linux with the TPM resource manager device (`/dev/tpmrm0`) or direct device (`/dev/tpm0`)
- `tpm2-tss` libraries installed (for the kernel resource manager)
- Go 1.22+ (for building)
- OpenBao with external KMS plugin support

### Verifying TPM Access

```bash
# Check TPM device exists
ls -la /dev/tpmrm0

# Check permissions (your OpenBao user needs read/write)
sudo usermod -aG tss openbao

# Verify TPM is functional
tpm2_getcap properties-fixed 2>/dev/null | head -5
```

## Building

```bash
# Build both the plugin and helper tool
make

# Build only the plugin binary
make plugin

# Build only the diagnostic helper
make helper

# Cross-compile for ARM64 (e.g., Raspberry Pi, Ampere)
make linux-arm64

# Print the plugin SHA-256 checksum (needed for OpenBao config)
make sha256

# Run tests (requires TPM or simulator)
make test
```

This produces two binaries:
- `bin/openbao-seal-tpm-plugin` -- the KMS plugin binary that OpenBao loads
- `bin/openbao-seal-tpm-helper` -- standalone diagnostic tool for testing TPM access

### RPM Package

An RPM spec is included for Fedora/RHEL packaging:

```bash
rpmbuild -ba openbao-seal-tpm.spec
```

The RPM installs the plugin to `/usr/lib64/openbao/plugins/` and the helper to `/usr/bin/`.

## Plugin Setup

This project ships as an **external KMS plugin** for OpenBao. No modifications to the OpenBao source code are required -- the plugin communicates with OpenBao over gRPC using the `go-kms-wrapping` plugin protocol.

### Step 1: Build and Install the Plugin

```bash
make plugin

# Copy to OpenBao's plugin directory
sudo cp bin/openbao-seal-tpm-plugin /etc/openbao/plugins/
sudo chown openbao:openbao /etc/openbao/plugins/openbao-seal-tpm-plugin
sudo chmod 755 /etc/openbao/plugins/openbao-seal-tpm-plugin
```

### Step 2: Get the Plugin SHA-256 Checksum

OpenBao requires a SHA-256 checksum for plugin integrity verification:

```bash
make sha256
# Or manually:
sha256sum bin/openbao-seal-tpm-plugin | cut -d' ' -f1
```

### Step 3: Configure OpenBao

Add the following to your OpenBao server configuration (`config.hcl`):

#### Minimal (SRK-only, no PCR binding)

```hcl
plugin_directory = "/etc/openbao/plugins"

plugin "kms" "tpm" {
  command   = "openbao-seal-tpm-plugin"
  sha256sum = "<output from make sha256>"
}

seal "tpm" {
  tpm_path  = "/dev/tpmrm0"
  key_label = "openbao-prod"
}
```

#### With PCR Binding (Measured Boot)

```hcl
plugin_directory = "/etc/openbao/plugins"

plugin "kms" "tpm" {
  command   = "openbao-seal-tpm-plugin"
  sha256sum = "<output from make sha256>"
}

seal "tpm" {
  tpm_path    = "/dev/tpmrm0"
  pcr_indexes = "0,2,7"
  key_label   = "openbao-prod"
}
```

**Note:** When used as a plugin, `pcr_indexes` is a comma-separated string (e.g., `"0,2,7"`) rather than an HCL list, because all plugin config values are passed as strings via the `go-kms-wrapping` config map.

### Step 4: Verify and Initialize

```bash
# Test TPM access first
openbao-seal-tpm-helper -test

# Start OpenBao
sudo systemctl start openbao

# Initialize (this seals the master key to the TPM)
openbao operator init
```

On subsequent restarts, OpenBao will automatically unseal using the TPM.

### Configuration Reference

| Parameter     | Type   | Default         | Description |
|---------------|--------|-----------------|-------------|
| `tpm_path`    | string | `/dev/tpmrm0`   | Path to the TPM device. Falls back to `/dev/tpm0` if the primary path fails. |
| `pcr_indexes` | string | `""` (none)     | Comma-separated PCR indexes to bind the seal to (e.g., `"0,2,7"`). Empty means SRK-only. |
| `key_label`   | string | `openbao-seal`  | Label stored with the sealed blob for identification. |

### Choosing PCR Indexes

| PCR | Measures | Stability |
|-----|----------|-----------|
| 0   | UEFI firmware code | Changes on firmware update |
| 1   | UEFI firmware config (BIOS settings) | Changes on BIOS config change |
| 2   | Option ROMs, UEFI drivers | Changes on firmware/add-in card update |
| 3   | Option ROM config | Rarely changes |
| 4   | MBR / bootloader code | Changes on bootloader update |
| 5   | GPT / partition table | Changes on disk layout change |
| 7   | Secure Boot policy | Changes on Secure Boot key enrollment |
| 8   | Kernel command line (grub) | Changes on grub config |
| 9   | initrd / kernel image (grub) | Changes on kernel update |

**Recommendations:**
- **Minimal**: `"7"` -- only Secure Boot policy. Survives kernel/firmware updates.
- **Moderate**: `"0,2,7"` -- firmware + Secure Boot. Catches firmware tampering.
- **Strict**: `"0,2,4,7,8,9"` -- full boot chain. Any OS or firmware change requires reseal.

## Helper Tool Usage

The `openbao-seal-tpm-helper` binary provides diagnostics and testing without running OpenBao.

### Read Current PCR Values

```bash
# Default PCRs 0-7
openbao-seal-tpm-helper -pcrs

# Specific PCRs
openbao-seal-tpm-helper -pcrs -pcr-indexes 0,2,7

# JSON output (for scripting)
openbao-seal-tpm-helper -pcrs -pcr-indexes 0,7 -json
```

Example output:

```
Current PCR Values (SHA-256)
============================
PCR[ 0]: a5b3c4d...
PCR[ 2]: 1f2e3d4...
PCR[ 7]: 9a8b7c6...

Note: Kernel/initrd updates will change PCR 4,8,9.
UEFI firmware updates will change PCR 0,2.
Bootloader changes will change PCR 4,5.
```

### Test Seal/Unseal

```bash
# Basic test (SRK-only)
openbao-seal-tpm-helper -test

# Test with PCR binding
openbao-seal-tpm-helper -test -pcr-indexes 0,7

# Override TPM device path
openbao-seal-tpm-helper -test -tpm-path /dev/tpm0
```

Example output:

```
TPM Seal/Unseal Test
====================
Device:      /dev/tpmrm0
PCR binding: [0 7]
Test data:   35 bytes

Sealing... OK (private=142 bytes, public=78 bytes)
Unsealing... OK -- data integrity verified
```

## Operations

### Initial Setup

```bash
# 1. Verify TPM access
openbao-seal-tpm-helper -test

# 2. Record current PCR values (if using PCR binding)
openbao-seal-tpm-helper -pcrs -pcr-indexes 0,2,7 -json > /etc/openbao/pcr-baseline.json

# 3. Add plugin + seal stanzas to OpenBao config (see Plugin Setup above)
# 4. Initialize OpenBao
openbao operator init
```

### After Kernel / Firmware Updates

If you use PCR binding, a kernel or firmware update will change PCR values and unseal will fail. You must **migrate the seal** after such updates:

```bash
# 1. Unseal manually with existing unseal keys
openbao operator unseal

# 2. Record new PCR values
openbao-seal-tpm-helper -pcrs -pcr-indexes 0,2,7

# 3. Trigger seal migration (OpenBao re-seals with current PCR values)
openbao operator seal
openbao operator unseal   # should now auto-unseal
```

If you use SRK-only (no PCR binding), kernel and firmware updates have no impact on auto-unseal.

### Troubleshooting

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `open TPM /dev/tpmrm0: permission denied` | OpenBao user lacks TPM access | `sudo usermod -aG tss openbao` and restart |
| `open TPM /dev/tpmrm0: no such file or directory` | No TPM or kernel module not loaded | `sudo modprobe tpm_tis` or check BIOS TPM settings |
| `unknown wrapper: tpm` | Plugin not registered in OpenBao config | Add `plugin "kms" "tpm" { ... }` block and set `plugin_directory` |
| `cannot execute files outside of configured plugin directory` | Plugin binary not in `plugin_directory` | Copy plugin binary to the configured `plugin_directory` path |
| Plugin checksum mismatch | `sha256sum` in config doesn't match binary | Re-run `make sha256` after rebuilding and update config |
| `TPM2_Unseal: ...` with PCR binding | Boot measurements changed | Manually unseal, then reseal (see above) |
| `TPM2_Create (seal): ...` | TPM in lockout / DA lockout | `tpm2_dictionarylockout --setup --max-tries=32 --clear-lockout` |

## Architecture

```
openbao-seal-tpm/
  seal/
    tpm_seal.go              # Core TPM seal/unseal logic
  wrapper/
    tpm_wrapper.go           # go-kms-wrapping Wrapper adapter
  cmd/
    openbao-seal-tpm-plugin/
      main.go                # KMS plugin binary (gRPC server)
    openbao-seal-tpm-helper/
      main.go                # Diagnostic CLI tool
  go.mod
  Makefile
  openbao-seal-tpm.spec      # RPM packaging
```

### Package Overview

- **`seal`** -- Core TPM 2.0 operations using `google/go-tpm`. Handles `TPM2_Create`/`TPM2_Unseal`, SRK management, PCR policy sessions. Independent of OpenBao.
- **`wrapper`** -- Adapts `seal.TPMSeal` to the `wrapping.Wrapper` interface from `openbao/go-kms-wrapping/v2`. Handles config parsing, JSON serialization of sealed blobs into `BlobInfo`, and key ID generation.
- **`cmd/openbao-seal-tpm-plugin`** -- Thin entry point that serves the wrapper over gRPC using `go-kms-wrapping/plugin/v2`. This is the binary OpenBao spawns.
- **`cmd/openbao-seal-tpm-helper`** -- Standalone tool for verifying TPM access and testing seal/unseal without OpenBao.

## Upstream Integration

This project currently works as an **external plugin** requiring no changes to OpenBao's source code. If there is community interest, it could be integrated into OpenBao core as a builtin seal type. The changes required would be minimal:

1. Add a `WrapperTypeTpm` constant to `go-kms-wrapping/v2/const.go`
2. Move the `wrapper` package into `go-kms-wrapping/wrappers/tpm/v2/`
3. Register the wrapper in OpenBao's `helper/kmsplugin/wrapper.go` builtin map

This would eliminate the need for the external plugin binary and `plugin` config block -- users would only need the `seal "tpm" { ... }` stanza, just like the existing `transit`, `awskms`, and `pkcs11` seal types.

Contributions and feedback welcome. If you're interested in seeing TPM auto-unseal in OpenBao core, please open an issue or PR at the [OpenBao repository](https://github.com/openbao/openbao).

## Security Considerations

- The sealed key is bound to the specific TPM chip. Moving the disk to another machine will not allow unseal.
- Without PCR binding, anyone who can boot the machine with the TPM can unseal. Physical security of the server is essential.
- With PCR binding, an attacker must also boot with the exact same firmware/kernel/bootloader chain. This mitigates evil-maid and boot-tampering attacks.
- The TPM's dictionary attack (DA) lockout protects against brute-force attempts on sealed objects.
- OpenBao verifies the plugin binary's SHA-256 checksum before executing it, preventing tampering.
- The plugin communicates with OpenBao over a mutual-TLS gRPC connection (auto-mTLS), so the channel is encrypted even on localhost.
- This is a **proof-of-concept**. It has not been audited. Do not use it for production workloads without thorough review and testing.

## License

MPL-2.0
