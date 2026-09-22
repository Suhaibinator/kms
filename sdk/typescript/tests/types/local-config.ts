import {
  type ConfigManager,
  LocalConfigManager,
  type ManagedConfigManager,
} from "@suhaibinator/kms/configstore";

const local: ConfigManager = new LocalConfigManager();
const managed = (manager: ManagedConfigManager): ConfigManager => manager;
void local;
void managed;
