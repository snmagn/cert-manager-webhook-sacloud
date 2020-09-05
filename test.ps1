docker image build --secret id=api-key,src=./testdata/my-custom-solver/api-key.yml --build-arg SKIP_VERIFY=false --build-arg TEST_ZONE_NAME=$env:TEST_ZONE_NAME -t "snmagn/sacloud-dns-webhook:dirty" .
