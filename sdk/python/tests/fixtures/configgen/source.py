from __future__ import annotations

from typing import Annotated

from pydantic import BaseModel, ConfigDict

from kms_paramstore.configstore import Parameter, SecretField
from kms_paramstore.secret import Secret


class ApplicationConfig(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True)

    port: Annotated[int, Parameter("runtime", reload="restart", views=("server",))] = 8080
    debug: Annotated[bool, Parameter("runtime", views=("server",))] = False
    password: Annotated[Secret, SecretField("db_password", views=("server",))]
