"""Minimal Pydantic managed-configuration model and binding."""

from typing import Annotated

from pydantic import BaseModel, ConfigDict

from kms_paramstore import Secret
from kms_paramstore.configstore import ConfigBinding, Parameter, SecretField


class AppConfig(BaseModel):
    model_config = ConfigDict(
        frozen=True, strict=True, extra="forbid", arbitrary_types_allowed=True
    )

    port: Annotated[int, Parameter("runtime", reload="restart")] = 8080
    feature_enabled: Annotated[bool, Parameter("features")] = False
    database_password: Annotated[Secret, SecretField("database_password")]


binding = ConfigBinding(AppConfig, {})
