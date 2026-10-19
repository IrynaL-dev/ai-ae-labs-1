# 1. Access Control Benchmark

## 1.1 Identity and Authentication
This section covers foundational identity controls, multi-factor authentication requirements, and session management policies for enterprise environments.

### 1.1.1 Password Complexity Policies
All enterprise systems must enforce strict password complexity rules to prevent brute-force attacks and credential stuffing.

| Control ID | Requirement Description | Severity |
| --- | --- | --- |
| CIS-1.1.1-A | Minimum password length must be at least 14 characters. | Critical |
| CIS-1.1.1-B | Passwords must disallow common dictionary words and patterns. | High |
| CIS-1.1.1-C | Mandatory password rotation every 90 days for standard accounts. | Medium |

## 1.2 Privileged Access Management
Managing administrator accounts securely is vital to maintaining overall cloud and infrastructure security posture.

### 1.2.1 Multi-Factor Authentication (MFA)
Multi-factor authentication adds an essential layer of security beyond traditional username and password combinations.

| Vendor / Service | MFA Enforced? | Compliance Status |
| --- | --- | --- |
| Active Directory | Yes (FIDO2 / TOTP) | Compliant |
| AWS IAM Roles | Yes (Hardware tokens) | Compliant |
| Legacy FTP Server | No | Non-Compliant |
