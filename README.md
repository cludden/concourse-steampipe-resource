# concourse-steampipe-resource
A [Concourse](https://concourse-ci.org/) resource for implementing a wide variety of triggers and data integrations via [Steampipe](https://steampipe.io/) and its expansive ecosystem of [plugins](https://hub.steampipe.io/plugins)

## Getting Started
```yaml
resource_types:
  - name: steampipe
    type: registry-image
    source:
      repository: ghcr.io/cludden/concourse-steampipe-resource
      username: ((ghcr.username))
      password: ((ghcr.password))

resources:
  # emits a version everytime the `foo` autoscaling group's launch configuration changes
  - name: aws-asg
    type: steampipe
    icon: aws
    check_every: 10m
    source:
      config: |
        connection "aws" {
          plugin  = "aws"
          profile = "target"
          regions = ["us-east-1"]
        }
      files:
        /home/steampipe/.aws/credentials: |
          [base]
          aws_access_key_id = ((aws.access_key))
          aws_secret_access_key = ((aws.secret_key))
          aws_session_token = ((aws.security_token))

          [target]
          duration_seconds = 900
          external_id = foo
          role_arn = arn:aws:iam::012345678910:role/foo
          role_session_name = concourse-steampipe-resource
          source_profile = base
      query: |
        select
          autoscaling_group_arn as arn,
          launch_configuration_name as launch_config
        from
          aws_ec2_autoscaling_group
        where
          name = 'foo';
      version_mapping: |
        # emit both an initial version, and a new version every time the launch configuration changes
        root = match {
          before.launch_config.or("unknown") != after.0.launch_config.or("unknown") => after.0
          _ => deleted()
        }

jobs:
  - name: handle-launch-config-change
    plan:
      # trigger when autoscaling group's launch configuration changes
      - get: aws-asg
        trigger: true

      - task: print
        config:
          platform: linux
          image_resource:
            type: registry-image
            source:
              repository: busybox
          inputs:
            - name: aws-asg
          run:
            path: /bin/bash
            args:
              - -c
              - |
                cat aws-asg/version.json
```

## Configuration

**Parameters:**
| Parameter | Type | Description | Required |
| :--- | :---: | :--- | :---: |
| config | `string` | Steampipe configuration | ✓ |
| debug | `bool` | enable debug logging | |
| files | `map[string]string` | map of additional files to write prior to invoking steampipe, can be used for configuring plugins that rely on canonical configuration files (e.g. `aws`) | |
| query | `string` | Steampipe query | ✓ |
| version_mapping | `string` | an optional [Bloblang mapping](https://www.benthos.dev/docs/guides/bloblang/about) that can be used to customize the versions emitted by the resource; the mapping receives as input a document with a `before` field that contains the previous version (if available), and an `after` field that contains the result of the query (note that this is typically an array of objects) | |

## Behavior

### `check`
Checks for new versions emitted via steampipe query

### `in`
Writes the JSON serialized version to the filesystem

**Files:**
- `version.json`

### `out`
Not implemented, will error if invoked via `put` step

## License
**UNLICENSED**
