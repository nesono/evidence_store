package api

import (
	"net/http"
)

// What a provisioner reads before it will provision anything.
//
// Entra fetches these three on its Test Connection and again at the start of a
// sync, and takes them literally: a server claiming support for filtering or
// PATCH it does not have gets sent requests it cannot answer, and one claiming
// less than it has is driven the slow way round. So these describe what this
// store actually does — including, in the case of the filter grammar, rather
// less than the specification allows.

const (
	schemaServiceProviderConfig = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	schemaResourceType          = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchema                = "urn:ietf:params:scim:schemas:core:2.0:Schema"
)

// ServiceProviderConfig says which optional parts of SCIM this store answers.
//
// The honest answers are mostly "no". Bulk, sort and ETags are not implemented
// and saying so is what keeps a client from trying; changePassword is not a
// question this store can be asked, since it holds no passwords.
func (h *SCIMHandler) ServiceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	scimJSON(w, http.StatusOK, map[string]any{
		"schemas": []string{schemaServiceProviderConfig},
		"patch":   map[string]any{"supported": true},
		"bulk":    map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter": map[string]any{
			"supported": true,
			// The page size a listing will not exceed however large a count is
			// asked for, so a client can size its own paging to match.
			"maxResults": maxPageSize,
		},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type":        "oauthbearertoken",
			"name":        "OAuth Bearer Token",
			"description": "An evidence store API key held by the provisioning client",
			"primary":     true,
		}},
		"meta": map[string]any{
			"resourceType": "ServiceProviderConfig",
			"location":     "/scim/v2/ServiceProviderConfig",
		},
	})
}

// ResourceTypes lists what can be provisioned here: people and groups.
func (h *SCIMHandler) ResourceTypes(w http.ResponseWriter, _ *http.Request) {
	types := []map[string]any{
		{
			"schemas":     []string{schemaResourceType},
			"id":          "User",
			"name":        "User",
			"endpoint":    "/Users",
			"description": "A person who may sign in and file evidence",
			"schema":      schemaUser,
			"meta": map[string]any{
				"resourceType": "ResourceType",
				"location":     "/scim/v2/ResourceTypes/User",
			},
		},
		{
			"schemas":     []string{schemaResourceType},
			"id":          "Group",
			"name":        "Group",
			"endpoint":    "/Groups",
			"description": "A directory group, whose name decides what its members may do here",
			"schema":      schemaGroup,
			"meta": map[string]any{
				"resourceType": "ResourceType",
				"location":     "/scim/v2/ResourceTypes/Group",
			},
		},
	}
	scimJSON(w, http.StatusOK, listResponse(types))
}

// Schemas describes the attributes this store keeps, and only those.
//
// Deliberately shorter than the specification's User schema. A client reads
// this to decide what to send, and advertising attributes that are accepted and
// then dropped would have a directory believing it had synchronised a job title
// this store has no column for.
func (h *SCIMHandler) Schemas(w http.ResponseWriter, _ *http.Request) {
	schemas := []map[string]any{
		{
			"schemas":     []string{schemaSchema},
			"id":          schemaUser,
			"name":        "User",
			"description": "A person who may sign in and file evidence",
			"attributes": []map[string]any{
				attribute("userName", "string", true, "readWrite",
					"The login name, unique across the store"),
				attribute("externalId", "string", false, "readWrite",
					"The provisioner's own identifier, echoed back and never matched on here"),
				attribute("displayName", "string", false, "readWrite",
					"The name shown beside their evidence"),
				attribute("active", "boolean", false, "readWrite",
					"False disables the account and ends every session it has open"),
				complexAttribute("name", "The parts a display name is composed from when one is not sent",
					attribute("givenName", "string", false, "readWrite", ""),
					attribute("familyName", "string", false, "readWrite", ""),
					attribute("formatted", "string", false, "readWrite", ""),
				),
				complexAttribute("emails", "The address their evidence is filed under",
					attribute("value", "string", false, "readWrite", ""),
					attribute("primary", "boolean", false, "readWrite", ""),
					attribute("type", "string", false, "readWrite", ""),
				),
			},
			"meta": map[string]any{
				"resourceType": "Schema",
				"location":     "/scim/v2/Schemas/" + schemaUser,
			},
		},
		{
			"schemas":     []string{schemaSchema},
			"id":          schemaGroup,
			"name":        "Group",
			"description": "A directory group, whose name decides what its members may do here",
			"attributes": []map[string]any{
				attribute("displayName", "string", true, "readWrite",
					"Looked up in the store's group-to-role map; an unmapped group grants nothing"),
				attribute("externalId", "string", false, "readWrite",
					"The provisioner's own identifier for the group"),
				complexAttribute("members", "The people in the group",
					attribute("value", "string", false, "readWrite", ""),
					attribute("display", "string", false, "readOnly", ""),
				),
			},
			"meta": map[string]any{
				"resourceType": "Schema",
				"location":     "/scim/v2/Schemas/" + schemaGroup,
			},
		},
	}
	scimJSON(w, http.StatusOK, listResponse(schemas))
}

// listResponse wraps a fixed set of resources in the envelope a client expects.
// These three endpoints do not page: there are two resource types and two
// schemas, and there always will be.
func listResponse[T any](resources []T) map[string]any {
	return map[string]any{
		"schemas":      []string{schemaListResponse},
		"totalResults": len(resources),
		"startIndex":   1,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	}
}

// attribute describes one field. uniqueness is "server" only for userName,
// which is the one attribute this store will refuse a second copy of.
func attribute(name, typ string, required bool, mutability, description string) map[string]any {
	uniqueness := "none"
	if name == "userName" {
		uniqueness = "server"
	}
	attr := map[string]any{
		"name":        name,
		"type":        typ,
		"multiValued": false,
		"required":    required,
		"caseExact":   false,
		"mutability":  mutability,
		"returned":    "default",
		"uniqueness":  uniqueness,
	}
	if description != "" {
		attr["description"] = description
	}
	return attr
}

func complexAttribute(name, description string, sub ...map[string]any) map[string]any {
	return map[string]any{
		"name":          name,
		"type":          "complex",
		"multiValued":   name == "emails" || name == "members",
		"required":      false,
		"mutability":    "readWrite",
		"returned":      "default",
		"description":   description,
		"subAttributes": sub,
	}
}
