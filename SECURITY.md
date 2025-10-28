# Security Documentation

This document provides a comprehensive overview of Sendy's security architecture, cryptographic implementation, and known limitations.

## Overview

Sendy implements end-to-end encryption for all peer-to-peer communications. The security model is designed to protect message content and file transfers from eavesdropping, while using a centralized router only for initial WebRTC signaling.

**⚠️ Important Notice:** Sendy is experimental software. While it implements strong cryptography, it has not undergone a professional security audit. Use at your own risk for non-critical communications.

## Cryptographic Implementation

### Identity and Authentication

**Ed25519 Digital Signatures**
- 256-bit security level
- Used for peer identity and router authentication
- Each peer has a unique Ed25519 keypair stored in `~/.sendy/data/key`
- Public key serves as peer ID (64 hex characters)
- Private key must be protected with file permissions 0600

**Key Generation:**
```go
pubkey, privkey, _ := ed25519.GenerateKey(rand.Reader)
```

### End-to-End Encryption

**NaCl/box (Curve25519 + XSalsa20-Poly1305)**
- Used for all P2P message and file encryption
- Ed25519 keys are converted to Curve25519 encryption keys
- Provides both confidentiality and authenticity
- 256-bit key strength

**Key Derivation:**
```go
// Convert Ed25519 signing keys to Curve25519 encryption keys
encPubKey := ed25519.PublicKey(pubkey).ToCurve25519()
encPrivKey := ed25519.PrivateKey(privkey).ToCurve25519()
```

**Encryption Process:**
1. Random 24-byte nonce generated for each message
2. Message encrypted with NaCl/box using recipient's public key and sender's private key
3. Nonce prepended to ciphertext
4. Authentication tag automatically added by NaCl/box

**Message Format:**
```
[24 bytes nonce][encrypted payload][16 bytes authentication tag]
```

### Router Authentication

All messages to/from the router are signed with Ed25519:
- Prevents impersonation attacks
- Router verifies each message signature
- Invalid signatures are rejected

**However:** Router can still see connection metadata (who connects to whom, message sizes, timing).

## Trust Model

### Trust On First Use (TOFU)

Sendy uses a Trust On First Use model similar to SSH:

1. **First Connection:** When connecting to a new peer, their Curve25519 public key is saved automatically
2. **Subsequent Connections:** The saved key is used for all future communications with that peer
3. **No Key Verification:** There is no built-in mechanism to verify peer keys out-of-band

**Implications:**
- ✅ Protects against eavesdropping after first connection
- ⚠️ Vulnerable to MITM attack during first connection
- ⚠️ No way to detect if a peer's key has changed (could indicate compromise or MITM)

**Recommendation:** For high-security communications, verify peer IDs through a separate secure channel (phone call, in person, encrypted message on another platform).

## What is Protected

### ✅ Encrypted

1. **P2P Messages:** All chat messages between peers
2. **File Transfers:** Complete file contents up to 200MB
3. **WebRTC Signaling:** SDP offers/answers and ICE candidates sent through router
4. **KEY_EXCHANGE:** Initial key exchange messages

### ❌ Not Protected (Metadata)

The router can observe:
1. **Connection Metadata:** Which peers connect to each other
2. **Message Timing:** When messages are sent (but not content)
3. **Message Sizes:** Approximate size of encrypted messages
4. **Online Status:** When peers are online/offline
5. **Connection Patterns:** Frequency and duration of communications

**Note:** Router cannot decrypt message contents, but metadata analysis can still reveal patterns.

## Known Limitations and Risks

### ⚠️ Critical Limitations

1. **No Perfect Forward Secrecy (PFS)**
   - If your private key is compromised, ALL past conversations can be decrypted
   - There is no session key rotation
   - Mitigation: Protect your `~/.sendy/data/key` file carefully (permissions 0600)

2. **MITM Vulnerability on First Connection**
   - TOFU model means first key exchange is vulnerable to man-in-the-middle attack
   - If router is compromised during first connection, it could substitute peer keys
   - Mitigation: Verify peer IDs out-of-band for sensitive contacts

3. **Router Metadata Visibility**
   - Router sees who talks to whom and when
   - Traffic analysis possible even without decrypting content
   - Mitigation: Run your own router server if metadata privacy is critical

4. **No Key Rotation**
   - Same encryption keys used for entire lifetime of contact relationship
   - Compromised keys cannot be easily rotated
   - Mitigation: Delete and re-add contact to generate new key exchange

5. **Key Storage**
   - Private key stored unencrypted on disk
   - Physical access to device = key compromise
   - Mitigation: Use full disk encryption, protect device physically

### ⚠️ Additional Considerations

6. **No Multi-Device Support**
   - Each device has its own keypair
   - No secure way to sync keys across devices
   - Messages sent to one device cannot be read on another

7. **No Key Verification**
   - No built-in fingerprint verification
   - No way to detect key changes
   - Mitigation: Compare peer IDs manually over secure channel

8. **No Repudiation**
   - Messages are cryptographically authenticated
   - Recipient can prove sender wrote message
   - Consider: This may be a feature or bug depending on your threat model

9. **Contact Blocking is Local Only**
   - Blocked contacts don't know they're blocked
   - Blocked peers can still send messages (they're just dropped)
   - Router still sees connection attempts from blocked peers

## Threat Model

### What Sendy Protects Against

✅ **Eavesdropping:** Passive network observer cannot read message content
✅ **Router Compromise:** Router cannot decrypt messages or files
✅ **Impersonation:** Cryptographic authentication prevents peer impersonation
✅ **Message Tampering:** Authentication tags prevent undetected modification
✅ **Replay Attacks:** Unique nonces prevent message replay

### What Sendy Does NOT Protect Against

❌ **First Connection MITM:** Compromised router can substitute keys initially
❌ **Private Key Theft:** Physical device access or malware can steal keys
❌ **Endpoint Compromise:** Malware on your device can read plaintext messages
❌ **Metadata Analysis:** Router sees connection patterns and timing
❌ **Traffic Confirmation:** Router can correlate messages between peers
❌ **Forward Secrecy:** Past messages compromised if key is stolen

## Security Best Practices

### For Users

1. **Protect Your Private Key**
   - Never share your `~/.sendy/data/key` file
   - Use full disk encryption
   - Set file permissions to 0600 (done automatically)

2. **Verify Peer IDs for Sensitive Contacts**
   - Exchange peer IDs over a secure secondary channel
   - Phone call, in person, or pre-established secure messaging
   - Critical for high-security communications

3. **Run Your Own Router**
   - If metadata privacy is important, run your own router server
   - Router source code is auditable
   - Reduces trust in third-party infrastructure

4. **Use Secure STUN Servers**
   - Default servers (Google, Cloudflare, Twilio) are generally trustworthy
   - For paranoid use cases, run your own STUN server
   - Use `--stun-servers` flag to specify custom servers

5. **Regular Key Rotation (Manual)**
   - Periodically delete and re-add critical contacts
   - This generates new encryption keys
   - Helps limit damage from potential undetected compromise

### For Developers

1. **Never Commit Private Keys**
   - The `.gitignore` excludes `data/key` files
   - Be careful with test keys

2. **Audit Dependencies**
   - Sendy uses well-vetted crypto libraries (Go crypto, NaCl)
   - Keep dependencies updated for security patches

3. **Review Router Code**
   - Router has access to metadata
   - Ensure router implementation doesn't log sensitive information

## Comparison to Other Systems

### Signal
✅ Has: PFS (Double Ratchet), key verification, sealed sender (partial metadata protection)
❌ Missing in Sendy: PFS, key verification, metadata protection

### SSH
✅ Similar: TOFU trust model, Ed25519 authentication
❌ SSH typically used in controlled environments; Sendy operates over internet

### PGP/GPG
✅ Similar: Long-term key pairs, no PFS by default
❌ Different: Sendy has synchronous communication, PGP is asynchronous

## Roadmap (Security Improvements)

Planned security enhancements:

1. **Perfect Forward Secrecy (PFS)**
   - Implement Double Ratchet or similar protocol
   - Session keys that are rotated regularly
   - Priority: HIGH

2. **Key Verification**
   - QR code scanning for peer ID verification
   - Safety numbers like Signal
   - Priority: HIGH

3. **Key Rotation**
   - Automatic periodic key rotation
   - Manual key rotation command
   - Priority: MEDIUM

4. **Encrypted Key Storage**
   - Optional passphrase protection for private key
   - Requires entering passphrase on startup
   - Priority: MEDIUM

5. **Multi-Device Support**
   - Secure key synchronization across devices
   - Linked devices model
   - Priority: LOW

## Reporting Security Issues

If you discover a security vulnerability in Sendy:

1. **Do NOT open a public GitHub issue**
2. Contact the maintainer privately (see GitHub profile for contact)
3. Provide details: affected version, reproduction steps, impact assessment
4. Allow reasonable time for fix before public disclosure

## Audits

**Status:** Sendy has NOT undergone professional security audit.

If you are a security researcher or organization interested in auditing Sendy, please reach out.

## License and Warranty

Sendy is provided under the MIT License with NO WARRANTY. See LICENSE file.

The developers make no guarantees about the security of this software. Use at your own risk.

---

**Last Updated:** 2025-10-29
**Version:** 1.0.0
