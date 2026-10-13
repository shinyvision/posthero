package demo

// The demo shop's GraphQL schema, as an introspection query answers it.

type tref map[string]any

func named(kind, name string) tref { return tref{"kind": kind, "name": name, "ofType": nil} }
func nonNull(t tref) tref          { return tref{"kind": "NON_NULL", "name": nil, "ofType": t} }
func list(t tref) tref             { return tref{"kind": "LIST", "name": nil, "ofType": t} }

var (
	tID     = named("SCALAR", "ID")
	tString = named("SCALAR", "String")
	tInt    = named("SCALAR", "Int")
	tFloat  = named("SCALAR", "Float")
)

func arg(name string, t tref, desc string) map[string]any {
	return map[string]any{"name": name, "description": desc, "type": t, "defaultValue": nil}
}

func field(name string, t tref, desc string, args ...map[string]any) map[string]any {
	if args == nil {
		args = []map[string]any{}
	}
	return map[string]any{"name": name, "description": desc, "args": args, "type": t, "isDeprecated": false, "deprecationReason": nil}
}

func object(name, desc string, fields ...map[string]any) map[string]any {
	return map[string]any{"kind": "OBJECT", "name": name, "description": desc, "fields": fields, "inputFields": nil, "interfaces": []any{}, "enumValues": nil, "possibleTypes": nil}
}

func scalar(name string) map[string]any {
	return map[string]any{"kind": "SCALAR", "name": name, "description": nil, "fields": nil, "inputFields": nil, "interfaces": nil, "enumValues": nil, "possibleTypes": nil}
}

// introspection is the answer to an introspection query.
func introspection() map[string]any {
	product := named("OBJECT", "Product")
	order := named("OBJECT", "Order")
	types := []map[string]any{
		object("Query", "What can be read.",
			field("products", nonNull(list(nonNull(product))), "The catalogue, first ones first.", arg("first", tInt, "How many; all when left out.")),
			field("product", product, "One product.", arg("id", nonNull(tID), "The product's id, like p-1001.")),
			field("me", named("OBJECT", "User"), "Who the token is for, and their orders."),
		),
		object("Mutation", "What can be changed.",
			field("createOrder", order, "Orders a product.", arg("input", nonNull(named("INPUT_OBJECT", "OrderInput")), "")),
		),
		object("Product", "Something the shop sells.",
			field("id", nonNull(tID), ""), field("name", nonNull(tString), ""),
			field("price", nonNull(tFloat), "In euros."), field("stock", nonNull(tInt), "How many are left."),
		),
		object("Order", "An order of one product.",
			field("id", nonNull(tID), ""), field("productId", nonNull(tID), ""), field("quantity", nonNull(tInt), ""),
			field("note", tString, ""), field("total", nonNull(tFloat), "Price times quantity."), field("created", nonNull(tString), "When, in RFC 3339."),
		),
		object("User", "Someone who logged in.",
			field("username", nonNull(tString), ""), field("orders", nonNull(list(nonNull(order))), "Their orders, oldest first."),
		),
		{"kind": "INPUT_OBJECT", "name": "OrderInput", "description": "What to order.", "fields": nil, "interfaces": nil, "enumValues": nil, "possibleTypes": nil,
			"inputFields": []map[string]any{arg("productId", nonNull(tID), ""), arg("quantity", nonNull(tInt), "At least 1."), arg("note", tString, "For the warehouse.")}},
		scalar("ID"), scalar("String"), scalar("Int"), scalar("Float"), scalar("Boolean"),
	}
	return map[string]any{"data": map[string]any{"__schema": map[string]any{
		"queryType": map[string]any{"name": "Query"}, "mutationType": map[string]any{"name": "Mutation"}, "subscriptionType": nil,
		"types": types,
	}}}
}
