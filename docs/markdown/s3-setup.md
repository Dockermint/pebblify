# S3 Setup

Configure Pebblify daemon to upload converted snapshot archives to an S3-compatible bucket.

## How it works

When `save.s3.enable = true`, the daemon uploads the repacked output archive to the configured S3 bucket after each successful conversion. Uploads use the AWS SDK v2 `PutObject` single-shot path (per `internal/daemon/store/s3/s3.go`). The daemon imports only three aws-sdk-go-v2 sub-modules: `config`, `credentials`, and `service/s3`.

Multiple save targets can be active simultaneously. The daemon iterates enabled stores (local, scp, s3) sequentially. A failure on one store does not prevent uploads to the remaining stores.

## Step 1: Create an S3 bucket

Create a bucket in the AWS region where your node operates. Enable versioning if you want to retain prior snapshots.

```bash
aws s3api create-bucket \
  --bucket pebblify-snapshots \
  --region us-east-1
```

For regions other than `us-east-1`, add the `--create-bucket-configuration` flag:

```bash
aws s3api create-bucket \
  --bucket pebblify-snapshots \
  --region eu-west-1 \
  --create-bucket-configuration LocationConstraint=eu-west-1
```

## Step 2: Create an IAM policy

Create a policy that grants the daemon the minimum permissions required to upload objects.

Save the following as `pebblify-s3-policy.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PebblifyUpload",
      "Effect": "Allow",
      "Action": [
        "s3:PutObject"
      ],
      "Resource": "arn:aws:s3:::pebblify-snapshots/*"
    },
    {
      "Sid": "PebblifyBucketAccess",
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": "arn:aws:s3:::pebblify-snapshots"
    }
  ]
}
```

Apply the policy:

```bash
aws iam create-policy \
  --policy-name PebblifyS3Upload \
  --policy-document file://pebblify-s3-policy.json
```

## Step 3: Create an IAM user and access key

```bash
# Create user
aws iam create-user --user-name pebblify-daemon

# Attach policy
aws iam attach-user-policy \
  --user-name pebblify-daemon \
  --policy-arn arn:aws:iam::<ACCOUNT_ID>:policy/PebblifyS3Upload

# Create access key
aws iam create-access-key --user-name pebblify-daemon
```

The `create-access-key` response returns `AccessKeyId` and `SecretAccessKey`. Copy both values immediately — the secret key is not retrievable again.

## Step 4: Set the secret key as an environment variable

The secret key must never be stored in `config.toml`. Set it as an environment variable:

```bash
export PEBBLIFY_S3_SECRET_KEY="your-secret-access-key"
```

For systemd deployments, add it to `/etc/pebblify/.env`:

```ini
PEBBLIFY_S3_SECRET_KEY=your-secret-access-key
```

For Podman Quadlet deployments, add it to `~/.pebblify/.env`.

## Step 5: Configure config.toml

```toml
[save.s3]
enable = true
bucket_name = "pebblify-snapshots"
s3_access_key = "AKIAIOSFODNN7EXAMPLE"
save_path = "converted"
```

| Field          | Description                                                                            |
| :------------- | :------------------------------------------------------------------------------------- |
| `bucket_name`  | The S3 bucket name. Must exist before the daemon starts.                              |
| `s3_access_key`| The AWS access key ID (public part). Safe to store in config.toml.                   |
| `save_path`    | Key prefix for uploaded objects. Final S3 key: `<save_path>/<filename>`. May be empty. |

With the example above, a converted archive named `gaia-20260418_pebbledb_1713427200.tar.lz4` would be uploaded to:

```
s3://pebblify-snapshots/converted/gaia-20260418_pebbledb_1713427200.tar.lz4
```

## Step 6: Set the AWS region

The daemon resolves the region in this order (per `internal/daemon/store/s3/s3.go:resolveRegion`):

1. `AWS_REGION` environment variable
2. `AWS_DEFAULT_REGION` environment variable
3. AWS SDK default config chain (`~/.aws/config`)
4. Falls back to `us-east-1` with a WARN log

Set the region explicitly to avoid the fallback:

```bash
export AWS_REGION="us-east-1"
```

## Compression and the output filename

The uploaded filename follows the pattern:

```
<original_name>_pebbledb_<unix_timestamp>.<extension>
```

Where `<extension>` is determined by `save.compression` in `config.toml`:

| `save.compression` | Output extension |
| :----------------- | :--------------- |
| `lz4`              | `.tar.lz4`       |
| `zstd`             | `.tar.zst`       |
| `gzip`             | `.tar.gz`        |
| `none`             | `.tar`           |
| `zip`              | `.zip`           |

## Verify uploads

After the daemon processes a job, verify the object was uploaded:

```bash
aws s3 ls s3://pebblify-snapshots/converted/
```

## Troubleshooting

| Symptom                              | Likely cause                                         | Fix                                                         |
| :----------------------------------- | :--------------------------------------------------- | :---------------------------------------------------------- |
| `s3: missing required secret`        | `PEBBLIFY_S3_SECRET_KEY` is unset                    | Export the variable before starting the daemon.            |
| `NoCredentialProviders`              | Access key ID is wrong or not found                  | Verify `s3_access_key` in `config.toml`.                   |
| `AccessDenied`                       | IAM policy does not grant `s3:PutObject` on the key  | Review and update the IAM policy.                          |
| `NoSuchBucket`                       | Bucket does not exist or wrong region                | Create the bucket or set `AWS_REGION` correctly.           |
| WARN: `s3 region not detected, falling back` | `AWS_REGION` not set, no shared config         | Set `export AWS_REGION=<your-region>`.                     |
