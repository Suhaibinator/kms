from __future__ import annotations

from enum import Enum
from typing import Annotated, Mapping

from pydantic import BaseModel, ConfigDict

from kms_paramstore.configstore import Parameter, SecretField
from kms_paramstore.secret import Secret
from .enums import Role


class Transport(str, Enum):
    HTTP = "http"
    HTTP_ALIAS = "http"
    GRPC = "grpc"


class ApplicationConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    port: Annotated[int, Parameter("runtime", reload="restart", views=("server",))] = 8080
    debug: Annotated[bool, Parameter("runtime", views=("server",))] = False
    weights: Annotated[dict[Role, int], Parameter("runtime", views=("server",))] = {Role.UNSPECIFIED: 0}
    transports: Annotated[Mapping[Transport, str] | None, Parameter("runtime")] = None
    password: Annotated[Secret, SecretField("db_password", views=("server",))] = Secret(
        bind_key="fixture-binding-key-never-generated"
    )
