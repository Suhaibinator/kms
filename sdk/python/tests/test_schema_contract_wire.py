from kms_paramstore._gen import kms_pb2


def test_schema_contract_presence_from_go_server_wire():
    unknown = kms_pb2.ConfigurationSchema.FromString(b"")
    # Produced by toProtoConfigurationSchema with an established empty contract.
    wire = b"\x50\x01"
    established = kms_pb2.ConfigurationSchema.FromString(wire)

    assert list(unknown.contract) == []
    assert not unknown.contract_established
    assert list(established.contract) == []
    assert established.contract_established
    assert established.SerializeToString() == wire
