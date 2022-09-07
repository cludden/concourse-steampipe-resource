# concourse-steampipe-resource
A [Concourse](https://concourse-ci.org/) resource for implementing a wide variety of triggers and data integrations via [Steampipe](https://steampipe.io/) and its expansive [plugin](https://hub.steampipe.io/plugins) ecosystem.

## Getting Started
```yaml
resource_types:
  # register steampipe resource type
  - name: steampipe
    type: registry-image
    source:
      repository: ghcr.io/cludden/concourse-steampipe-resource

resources:
  # configure steampipe resource that emits a version everytime the `foo` autoscaling 
  # group's launch configuration changes
  - name: aws-asg
    type: steampipe
    icon: aws
    check_every: 10m
    source:
      # configure aws connection (https://steampipe.io/docs/managing/connections)
      config: |
        connection "aws" {
          plugin  = "aws"
          profile = "target"
          regions = ["us-east-1"]
        }
      # populate aws shared credentials file to assume role (https://hub.steampipe.io/plugins/turbot/aws#assumerole-credentials-no-mfa)
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
      # define steampipe query to execute
      query: |
        select
          autoscaling_group_arn as arn,
          launch_configuration_name as launch_config
        from
          aws_ec2_autoscaling_group
        where
          name = 'foo'
        limit 1;

jobs:
  - name: handle-launch-config-change
    plan:
      # trigger when autoscaling group's launch configuration changes
      - get: aws-asg
        trigger: true
```

## Configuration

**Parameters:**
| Parameter | Type | Description | Required |
| :--- | :---: | :--- | :---: |
| archive | [*archive.Archive](https://pkg.go.dev/github.com/cludden/concourse-go-sdk@v0.3.1/pkg/archive#Config) | optional archive config that can be used to enable [resource version archiving](https://github.com/cludden/concourse-go-sdk#archiving) | |
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

## Plugins
The official image hosted at `ghcr.io/cludden/concourse-steampipe-resource` ships with the following Steampipe plugins installed:
- `aws`
- `code`
- `config`
- `datadog`
- `net`

To customize the installed plugins, build a derivative image.

```dockerfile
FROM ghcr.io/cludden/concourse-steampipe-resource

# install plugins as steampipe user and remove default configs
USER steampipe:0
RUN steampipe plugin install foo bar baz ...
RUN rm -rf /home/steampipe/.steampipe/config/*.spc
USER root
```

## Version Mapping
By default, the versions emitted by this resource take the shape of the first row returned by the configured query.
```
# query
select
  name,
  image_id
from
  aws_ec2_ami
order by
  creation_date desc
limit 1;

# sample version
{
  "image_id": "ami-9239492398498459",
  "name": "foo"
}
```

The shape of the emitted version can be lightly customized by tweaking the query definition.
```
# query
select
  name,
  image_id,
  mapping -> 'Ebs' ->> 'VolumeSize' as volume_size,
  mapping -> 'Ebs' ->> 'VolumeType' as volume_type,
  mapping -> 'Ebs' ->> 'Encrypted' as encryption_status,
  mapping -> 'Ebs' ->> 'KmsKeyId' as kms_key,
  mapping -> 'Ebs' ->> 'DeleteOnTermination' as delete_on_termination
from
  aws_ec2_ami
  cross join jsonb_array_elements(block_device_mappings) as mapping
order by
  creation_date desc
limit 1;

# sample version
{
  "delete_on_termination": "...",
  "encryption_status": "...",
  "image_id": "...",
  "kms_key": "...",
  "name": "foo",
  "volume_size": "...",
  "volume_type": "..."
}
```

However, sometimes you'll want to customize this behavior even further. This can be done by configuring the `version_mapping` source parameter which accepts a [Bloblang mapping](https://www.benthos.dev/docs/guides/bloblang/about). This mapping receives as input a document with a `before` field that contains the previous version (if available), and an `after` field that contains the result of the query (note that this is typically an array of objects). In the following example, we define a `query` that returns multiple rows, and a `version_mapping` that filters the rows to those whose name matches the name of the most recent image and then emit a version with a `name` key and an additional key with the ami id for each account/region combination.

```
# query
select
  account_id,
  image_id,
  name,
  region
from
  aws_ec2_ami_shared
where
  name like 'my-ami/%'
order by
  creation_date desc
limit 10;

# version_mapping
let name = after.0.name.not_empty()
let images = after.filter(image -> image.name == $name)
root = $images.fold({}, image -> image.tally.assign({
  "name": image.value.name,
  ("%s_%s".format(image.value.account_id, image.value.region)): image.value.image_id
}))

# sample version
{
  "name": "my-ami/9b996074-3341-454a-845d-7179eae004f0",
  "012345678901_us-east-1": "ami-9239492398498459",
  "012345678901_us-west-2": "ami-9348592974792937"
}
```

## License
Licensed under the [MIT-0 License](LICENSE.md)  
Copyright (c) 2022 Chris Ludden
