import Ajv2020 from "ajv/dist/2020.js";
import { applyAgentSchemaPolicy } from "./schema-policy.mjs";

// createSchemaValidator keeps canonical DTO validation and dynamic Tool
// validation on the same asserted formats and DayOrder schema extensions.
export function createSchemaValidator(): Ajv2020 {
  return applyAgentSchemaPolicy(new Ajv2020({ allErrors: true, strict: true }));
}
